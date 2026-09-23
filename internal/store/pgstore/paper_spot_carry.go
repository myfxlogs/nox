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
	ID              int64
	Venue           string
	Symbol          string
	Side            string // short_perp / long_perp
	EntryFundingAnn float64
	EntryTs         time.Time
	QtyUSD          float64
	LastFundingAnn  *float64
	AccruedUSD      float64
	LastAccrueTs    *time.Time
	LowStreak       int
	ExitTs          *time.Time
	ExitReason      string
	Status          string // active / exited / demo
	Note            string
	EntryBasisBps   *float64 // D-354 S2：开仓时 uni 当轮基差快照（NULL=当轮缺，传播不臆造）
	LastBasisBps    *float64 // D-354 S2：StepPaperSpotCarry 每轮随 uni 更新
	Structure       string   // D-374 S2：行结构（spot_carry / long_cfd_short_perp / short_cfd_long_perp；DB 缺省 spot_carry）
	MtVenue         *string  // D-374 S2：cfd 行 CFD 腿 venue（NULL=spot_carry）
	// D-410 S1：全账口径（KB nullable-column：可空列全指针扫描）。
	EstRoundTripFeeUSD *float64 // 开仓一次性记账往返费用估计（存正幅度；NULL=venue 未覆盖拒填行）
	ConvergenceUSD     *float64 // 退出冻结收敛捕获 dir×(last−entry)×qty/1e4（active 行 NULL）
}

// InsertPaperSpotCarry 开纸面仓（同 venue+symbol 活跃行唯一由部分唯一索引兜底；
// 撞唯一 → 返回 ErrPaperExists 由调用方跳过，不中断本轮。D-374：cfd 行 venue 传
// mt×cex 组合串+structure/mt_venue 落列；spot 轨调用方零改动——structure 空串补缺省）。
func (s *Store) InsertPaperSpotCarry(ctx context.Context, r *PaperSpotCarry) error {
	r.Status = "active"
	if r.Structure == "" {
		r.Structure = "spot_carry"
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO paper_spot_carry (venue, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, last_accrue_ts, entry_basis_bps, last_basis_bps, structure, mt_venue, est_round_trip_fee_usd)
		VALUES ($1,$2,$3,$4,$5,$6,now(),$7,$7,$8,$9,$10)
		ON CONFLICT (venue, symbol) WHERE status IN ('active','demo') DO NOTHING
		RETURNING id, status`,
		r.Venue, r.Symbol, r.Side, r.EntryFundingAnn, r.QtyUSD, r.EntryFundingAnn, r.EntryBasisBps, r.Structure, r.MtVenue, r.EstRoundTripFeeUSD).Scan(&r.ID, &r.Status)
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

// ListPaperSpotCarryBoard D-352 S1：纸面轨前端卡行源——active+demo 全量+近 window 内
// exited（exit_ts ≥ now−window；退出原因=「坑还是机会」的实证记录流，窗口防表无限长）。
func (s *Store) ListPaperSpotCarryBoard(ctx context.Context, recentWindow time.Duration) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry
		WHERE status IN ('active','demo') OR (status = 'exited' AND exit_ts >= now() - ($1::double precision * interval '1 second'))
		ORDER BY id`, recentWindow.Seconds())
}

// AccruePaperSpotCarry 逐轮累积：accrued += last_funding_ann/100/365×间隔天×qty；回写 last_funding_ann/last_accrue_ts/low_streak/last_basis_bps。
// lastBasisBps：D-354 S2 每轮随 uni 更新（nil=当轮缺，保持 NULL）。
func (s *Store) AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn float64, accruedAdd float64, lowStreak int, lastBasisBps *float64) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET accrued_usd = accrued_usd + $2, last_funding_ann = $3,
		    last_accrue_ts = now(), low_streak = $4, last_basis_bps = $5
		WHERE id = $1`, id, accruedAdd, lastFundingAnn, lowStreak, lastBasisBps)
	if err != nil {
		return fmt.Errorf("accrue paper spot carry %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("accrue paper spot carry %d: row gone", id)
	}
	return nil
}

// ErrPaperPromoteNoActive 无 active 纸面行可升级（该 symbol 已是 demo 行）——RPC 侧映射 already_exists。
var ErrPaperPromoteNoActive = errors.New("paper spot carry: no active row to promote")

// PromotePaperToDemo D-350R1 S2：纸面 active 行升级为 demo 实测仓（单 tx）——
// 旧行封账保留（status='exited'+exit_reason='promoted_demo'，纸面账历史不丢），
// 新行 status='demo'（note=双腿回执）。无 active 行可升 → ErrPaperPromoteNoActive。
func (s *Store) PromotePaperToDemo(ctx context.Context, venue, symbol string, r *PaperSpotCarry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("promote begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // 显式 Commit；失败路径 rollback
	ct, err := tx.Exec(ctx, `
		UPDATE paper_spot_carry SET status = 'exited', exit_ts = now(), exit_reason = 'promoted_demo'
		WHERE venue = $1 AND symbol = $2 AND status = 'active'`, venue, symbol)
	if err != nil {
		return fmt.Errorf("promote close active: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrPaperPromoteNoActive
	}
	r.Status = "demo"
	err = tx.QueryRow(ctx, `
		INSERT INTO paper_spot_carry (venue, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, status, note)
		VALUES ($1,$2,$3,$4,$5,$6,'demo',$7)
		RETURNING id`,
		venue, symbol, r.Side, r.EntryFundingAnn, r.QtyUSD, r.EntryFundingAnn, r.Note).Scan(&r.ID)
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

// LatestPaperExitTs D-410 S4：该 venue|symbol 键最近退出时点（max(exit_ts)，保底槽 flap
// 冷却重构源——列已在表上零新表；无退出行 → nil）。
func (s *Store) LatestPaperExitTs(ctx context.Context, venue, symbol string) (*time.Time, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(exit_ts) FROM paper_spot_carry
		WHERE venue = $1 AND symbol = $2 AND exit_ts IS NOT NULL`, venue, symbol).Scan(&ts)
	if err != nil {
		return nil, fmt.Errorf("latest paper exit ts %s|%s: %w", venue, symbol, err)
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

const paperSpotCarryCols = `id, venue, symbol, side, entry_funding_ann, entry_ts, qty_usd,
	last_funding_ann, accrued_usd, last_accrue_ts, low_streak, exit_ts, exit_reason, status, note,
	entry_basis_bps, last_basis_bps, structure, mt_venue, est_round_trip_fee_usd, convergence_usd`

func (s *Store) scanPaperSpotCarry(ctx context.Context, q string, args ...any) ([]PaperSpotCarry, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list paper spot carry: %w", err)
	}
	defer rows.Close()
	var out []PaperSpotCarry
	for rows.Next() {
		var r PaperSpotCarry
		if err := rows.Scan(&r.ID, &r.Venue, &r.Symbol, &r.Side, &r.EntryFundingAnn, &r.EntryTs,
			&r.QtyUSD, &r.LastFundingAnn, &r.AccruedUSD, &r.LastAccrueTs, &r.LowStreak,
			&r.ExitTs, &r.ExitReason, &r.Status, &r.Note,
			&r.EntryBasisBps, &r.LastBasisBps, &r.Structure, &r.MtVenue,
			&r.EstRoundTripFeeUSD, &r.ConvergenceUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
