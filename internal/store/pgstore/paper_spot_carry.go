// paper_spot_carry.go D-350 阶段②：spot-carry 纸面验证轨存储（开仓/累积/退出三态）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrPaperExists 同 venue+symbol 已有活跃纸面/demo 仓（调用方跳过，不中断扫描轮）。
var ErrPaperExists = errors.New("paper spot carry: active row exists")

// PaperSpotCarry paper_spot_carry 一行（纸面仓或 demo 仓）。
type PaperSpotCarry struct {
	ID int64
	// D-498：venue_a/venue_b 直持配对双列（spot_carry=venue_a 单臂 venue_b=''；
	// cfd=venue_a(MT)×venue_b(CEX)；mt×mt=双 MT）。Venue 字段退役——配对展示串由
	// VenuePair() 产出，消费端禁回解析。
	VenueA          string
	VenueB          string
	VenueTypeA      string // D-555：A 臂 venue_type（双CEX spot 腿=spot；mt×cex/存量行=''，venue_key 生成列投影裸域）
	VenueTypeB      string // D-555：B 臂 venue_type（双CEX perp 腿=perp；同上）
	Symbol          string
	Side            string // short_perp / long_perp
	EntryFundingAnn float64
	EntryTs         time.Time
	QtyUSD          float64
	LastFundingAnn  *float64
	AccruedUSD      float64
	// LastAccrueTs D-474a S1 语义钉死：「已结算至的边界 ts」（非上次写库时刻）——
	// 各腿结算边界取 min 共同前缀；缺席/缺事实轮不前推（缺席不计时）。
	LastAccrueTs  *time.Time
	LowStreak     int
	MissStreak    int // D-474a S4：连续缺席轮次（行在才清 0；与 low_streak 分家）
	ExitTs        *time.Time
	ExitReason    string
	Status        string // active / exited / demo
	Note          string
	EntryBasisBps *float64 // D-354 S2：开仓时 uni 当轮基差快照（NULL=当轮缺，传播不臆造）
	LastBasisBps  *float64 // D-354 S2：StepPaperSpotCarry 每轮随 uni 更新
	Structure     string   // D-374 S2：行结构（spot_carry / long_cfd_short_perp / short_cfd_long_perp；DB 缺省 spot_carry）
	MtVenue       *string  // D-374 S2：cfd 行 CFD 腿 venue（NULL=spot_carry）
	// D-410 S1：全账口径（KB nullable-column：可空列全指针扫描）。
	EstRoundTripFeeUSD *float64 // 开仓一次性记账往返费用估计（存正幅度；NULL=venue 未覆盖拒填行）
	ConvergenceUSD     *float64 // 退出冻结收敛捕获 dir×(last−entry)×qty/1e4（active 行 NULL）
	// D-475a S5 观测列：accrue_ticks=实结期数（结算对齐累积 UPDATE 每写一次+1）；
	// min_accrued_usd=存续期 accrued 最低水位（回撤观测，NULL=未记账）。
	AccrueTicks   int
	MinAccruedUSD *float64
}

// InsertPaperSpotCarry 开纸面仓（同 venue+symbol 活跃行唯一由部分唯一索引兜底；
// 撞唯一 → 返回 ErrPaperExists 由调用方跳过，不中断本轮。D-374：cfd 行 VenueA=MT 裸名、
// VenueB=CEX 裸名+structure/mt_venue 落列；spot 轨 VenueB=”——structure 空串补缺省）。
func (s *Store) InsertPaperSpotCarry(ctx context.Context, r *PaperSpotCarry) error {
	r.Status = "active"
	if r.Structure == "" {
		r.Structure = "spot_carry"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO paper_spot_carry (venue_a, venue_b, venue_type_a, venue_type_b, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, last_accrue_ts, entry_basis_bps, last_basis_bps, structure, mt_venue, est_round_trip_fee_usd, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$7,now(),$9,$9,$10,$11,$12,$13)
		ON CONFLICT (venue_a, venue_b, symbol) WHERE status IN ('active','demo') DO NOTHING
		RETURNING id, status`,
		r.VenueA, r.VenueB, r.VenueTypeA, r.VenueTypeB, r.Symbol, r.Side, r.EntryFundingAnn, r.QtyUSD, r.EntryBasisBps, r.Structure, r.MtVenue, r.EstRoundTripFeeUSD, r.Note).Scan(&r.ID, &r.Status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrPaperExists
		}
		return fmt.Errorf("insert paper spot carry: %w", err)
	}
	return nil
}

// ListActivePaperSpotCarry 活跃纸面仓（status='active'）。
func (s *Store) ListActivePaperSpotCarry(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status = 'active' ORDER BY id`)
}

// ListDemoPaperSpotCarry demo 实测仓（status='demo'，D-350 阶段③）。
func (s *Store) ListDemoPaperSpotCarry(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status = 'demo' ORDER BY id`)
}

// ListAccrablePaperSpotCarry 可累积仓（active+demo，D-350R1 S5：demo 行也累积）。
func (s *Store) ListAccrablePaperSpotCarry(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status IN ('active','demo') ORDER BY id`)
}

// paperExitRecentLimit  exited 行硬上限（热修 hotfix-paper：OOM pprof 实证——帧率级
// 开平产生数万垃圾行，本查询 5 万行×protojson+deflate=GB 级分配。active/demo 全量保留）。
const paperExitRecentLimit = 500

// ListPaperSpotCarryBoard D-352 S1：纸面轨前端卡行源——active+demo 全量+近 window 内
// exited 最近 paperExitRecentLimit 条（exit_ts 降序取新；窗口+上限双防表无限长）。
func (s *Store) ListPaperSpotCarryBoard(ctx context.Context, recentWindow time.Duration) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM (
		SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status IN ('active','demo')
		UNION ALL
		(SELECT `+paperSpotCarryCols+` FROM paper_spot_carry
		 WHERE status = 'exited' AND exit_ts >= now() - ($1::double precision * interval '1 second')
		 ORDER BY exit_ts DESC LIMIT `+fmt.Sprint(paperExitRecentLimit)+`)
	) t ORDER BY id`, recentWindow.Seconds())
}

// AccruePaperSpotCarry 逐轮累积：accrued += accruedAdd（D-474a S1 起=已结算边界逐期实率
// 累积，非快照外推）；回写 last_funding_ann/last_accrue_ts/low_streak/miss_streak/last_basis_bps。
// accrueTo：本轮结算对齐推进到的边界 ts（语义=「已结算至」；nil=保持列值不动——
// 存量行回迁哨兵 NULL 或缺席轮不前推水位用）。
// lastBasisBps：D-354 S2 每轮随 uni 更新；nil=当轮缺→保持 last-good 不覆写
// （瞬断≠永损——断供事实由 miss_streak/coverage 表达，basis 列非载体）。
// accruedReplace=true 时 accrued_usd 直接置 accruedAdd（D-474a S1 存量行重算覆盖写）。
// D-475a S5：accrue_ticks 只在 accrueTo 非 nil（实结轮）+1；min_accrued_usd 随写取低水位。
func (s *Store) AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn float64, accruedAdd float64, lowStreak, missStreak int, lastBasisBps *float64, accrueTo *time.Time, accruedReplace bool) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET accrued_usd = CASE WHEN $8 THEN $2 ELSE accrued_usd + $2 END, last_funding_ann = $3,
		    last_accrue_ts = COALESCE($7, last_accrue_ts), low_streak = $4, miss_streak = $6, last_basis_bps = COALESCE($5, last_basis_bps),
		    accrue_ticks = accrue_ticks + CASE WHEN $7 IS NULL THEN 0 ELSE 1 END, -- 实结期数：缺席/哨兵轮（accrueTo=nil）不计
		    min_accrued_usd = LEAST(COALESCE(min_accrued_usd, CASE WHEN $8 THEN $2 ELSE accrued_usd + $2 END),
		                            CASE WHEN $8 THEN $2 ELSE accrued_usd + $2 END)
		WHERE id = $1`, id, accruedAdd, lastFundingAnn, lowStreak, lastBasisBps, missStreak, accrueTo, accruedReplace)
	if err != nil {
		return fmt.Errorf("accrue paper spot carry %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("accrue paper spot carry %d: row gone", id)
	}
	return nil
}

// AnchorPaperEntryBasis D-537②：entry_basis_bps NULL 行补锚——写入首个实测簿值
// （簿后生覆盖场景：monitor 簿首行晚于 entry_ts，ts<=entry 锚谓词永空）。
// est_round_trip_fee_usd 同拍补（仍 NULL 时；spread=锚值同源）。IS NULL 幂等，
// 已锚行不受影响。返回是否实写。
func (s *Store) AnchorPaperEntryBasis(ctx context.Context, id int64, entryBps float64, estFee *float64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET entry_basis_bps=$2, est_round_trip_fee_usd=COALESCE(est_round_trip_fee_usd,$3)
		WHERE id=$1 AND entry_basis_bps IS NULL`, id, entryBps, estFee)
	if err != nil {
		return false, fmt.Errorf("anchor paper entry basis %d: %w", id, err)
	}
	return ct.RowsAffected() > 0, nil
}

// ErrPaperPromoteNoActive 无 active 纸面行可升级（该 symbol 已是 demo 行）——RPC 侧映射 already_exists。
var ErrPaperPromoteNoActive = errors.New("paper spot carry: no active row to promote")

// PromotePaperToDemo D-350R1 S2：纸面 active 行升级为 demo 实测仓（单 tx）——
// 旧行封账保留（status='exited'+exit_reason='promoted_demo'，纸面账历史不丢），
// 新行 status='demo'（note=双腿回执）。无 active 行可升 → ErrPaperPromoteNoActive。
func (s *Store) PromotePaperToDemo(ctx context.Context, venueA, venueB, symbol string, r *PaperSpotCarry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("promote begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // 显式 Commit；失败路径 rollback
	ct, err := tx.Exec(ctx, `
		UPDATE paper_spot_carry SET status = 'exited', exit_ts = now(), exit_reason = 'promoted_demo'
		WHERE venue_a = $1 AND venue_b = $2 AND symbol = $3 AND status = 'active'`, venueA, venueB, symbol)
	if err != nil {
		return fmt.Errorf("promote close active: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrPaperPromoteNoActive
	}
	r.Status = "demo"
	err = tx.QueryRow(ctx, `
		INSERT INTO paper_spot_carry (venue_a, venue_b, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, status, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'demo',$8)
		RETURNING id`,
		venueA, venueB, symbol, r.Side, r.EntryFundingAnn, r.QtyUSD, r.EntryFundingAnn, r.Note).Scan(&r.ID)
	if err != nil {
		return fmt.Errorf("promote insert demo: %w", err)
	}
	return tx.Commit(ctx)
}

// MarkPaperSpotCarryDemo 纸面行标记为 demo 实测仓（阶段③：记录双腿回执 note）。
func (s *Store) MarkPaperSpotCarryDemo(ctx context.Context, id int64, note string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry SET status = 'demo', note = $2 WHERE id = $1`, id, note)
	if err != nil {
		return fmt.Errorf("mark paper spot carry demo %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("mark paper spot carry demo %d: row gone", id)
	}
	return nil
}

// AnnotatePaperNote D-474c S1：demo 行实收口径注记——marker 幂等追加（已含不叠注；
// 行消失时静默=accrue 写刚成功、竞态删行边缘态不升级为错误）。
func (s *Store) AnnotatePaperNote(ctx context.Context, id int64, marker string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry SET note = note || ' | ' || $2
		WHERE id = $1 AND position($2 in note) = 0`, id, marker); err != nil {
		return fmt.Errorf("annotate paper note %d: %w", id, err)
	}
	return nil
}

// LatestPaperExitTs D-410 S4：该 venue|symbol 键最近退出时点（max(exit_ts)，保底槽 flap
// 冷却重构源——列已在表上零新表；无退出行 → nil）。
func (s *Store) LatestPaperExitTs(ctx context.Context, venueA, venueB, symbol string) (*time.Time, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(exit_ts) FROM paper_spot_carry
		WHERE venue_a = $1 AND venue_b = $2 AND symbol = $3 AND exit_ts IS NOT NULL`, venueA, venueB, symbol).Scan(&ts)
	if err != nil {
		return nil, fmt.Errorf("latest paper exit ts %s×%s|%s: %w", venueA, venueB, symbol, err)
	}
	return ts, nil
}

// ExitPaperSpotCarry 退出（reason='normalized'/'age_7d'）。
// D-410 S2：convergenceUSD 退出时冻结收敛捕获（nil→列保持 NULL=缺账诚实缺）。
func (s *Store) ExitPaperSpotCarry(ctx context.Context, id int64, reason string, convergenceUSD *float64) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET status = 'exited', exit_ts = now(), exit_reason = $2, convergence_usd = $3
		WHERE id = $1 AND status = 'active'`, id, reason, convergenceUSD)
	if err != nil {
		return fmt.Errorf("exit paper spot carry %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("exit paper spot carry %d: not active", id)
	}
	return nil
}

// D-498：venue_a/venue_b 直出（删 '×' 合成臂——配对展示串由 VenuePair() 产出，Go 侧断解析）。
const paperSpotCarryCols = `id, venue_a, venue_b, venue_type_a, venue_type_b, symbol, side, entry_funding_ann, entry_ts, qty_usd,
	last_funding_ann, accrued_usd, last_accrue_ts, low_streak, miss_streak, exit_ts, exit_reason, status, note,
	entry_basis_bps, last_basis_bps, structure, mt_venue, est_round_trip_fee_usd, convergence_usd,
	accrue_ticks, min_accrued_usd`

// LatestPaperByLegs D-475a S7：canonical+两腿裸名回查最近纸面行——
// venue 组合串（'3001157×okx'）含两腿裸名即匹配；无行→nil 非错误（调用方跳过）。
func (s *Store) LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*PaperSpotCarry, error) {
	if len(legVenues) == 0 {
		return nil, nil
	}
	q := `SELECT ` + paperSpotCarryCols + ` FROM paper_spot_carry
		WHERE symbol = $1 AND status IN ('active','demo','exited')`
	args := []any{canonical}
	for i, v := range legVenues {
		q += fmt.Sprintf(` AND (venue_a = $%d OR venue_b = $%d)`, i+2, i+2)
		args = append(args, v)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	rows, err := s.scanPaperSpotCarry(ctx, q, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (s *Store) scanPaperSpotCarry(ctx context.Context, q string, args ...any) ([]PaperSpotCarry, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list paper spot carry: %w", err)
	}
	defer rows.Close()
	var out []PaperSpotCarry
	for rows.Next() {
		var r PaperSpotCarry
		if err := rows.Scan(&r.ID, &r.VenueA, &r.VenueB, &r.VenueTypeA, &r.VenueTypeB, &r.Symbol, &r.Side, &r.EntryFundingAnn, &r.EntryTs,
			&r.QtyUSD, &r.LastFundingAnn, &r.AccruedUSD, &r.LastAccrueTs, &r.LowStreak, &r.MissStreak,
			&r.ExitTs, &r.ExitReason, &r.Status, &r.Note,
			&r.EntryBasisBps, &r.LastBasisBps, &r.Structure, &r.MtVenue,
			&r.EstRoundTripFeeUSD, &r.ConvergenceUSD, &r.AccrueTicks, &r.MinAccruedUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// VenuePair 配对展示串（venue_b 空→venue_a 单臂；否则 'a×b'）——展示/map 键用，
// 与 carry_graduations.venue 生成列同构。键域语义取 VenueA/VenueB 字段，禁回解析本串。
func (r PaperSpotCarry) VenuePair() string {
	if r.VenueB == "" {
		return r.VenueA
	}
	return r.VenueA + "×" + r.VenueB
}
