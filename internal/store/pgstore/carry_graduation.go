// carry_graduation.go D-403 S1/S2：验证毕业链建议表存取（扫描→验证→实盘三段接通）。
// 证据列=建议时点快照（KB carry-rate-is-not-expectancy：授权决策看证据快照不搬速率）；
// combo_id=approve 写入的 xasset_allow_combos.id（NULL=未写/非 cfd 体检分支）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/keydomain"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrXAssetComboExists approve 写组合撞唯一键 (canonical_buy,canonical_sell,venue_buy,venue_sell)
// —— 调用方（service）报「已在白名单」，不视作失败。
var ErrXAssetComboExists = errors.New("xasset allow combo: already exists")

// CarryGraduation carry_graduations 一行（毕业建议+裁决态）。
type CarryGraduation struct {
	ID             int64
	PaperID        int64
	Venue          string // D-498：展示性配对投影（generated 列；语义键用 VenueA/VenueB）
	VenueA         string
	VenueB         string
	VenueTypeA     string // D-555：A 臂 venue_type（源 paper 行透传；存量行=''）
	VenueTypeB     string // D-555：B 臂 venue_type（同上）
	Symbol         string
	Structure      string
	MtVenue        *string // cfd 行 MT 腿 venue（raw id）；非 cfd NULL
	AgeDays        float64
	AccruedUSD     float64
	LastFundingAnn *float64
	NetAllInUSD    *float64 // D-410 S5：建议时点全账净额（accrued−est_fee+convergence；NULL=缺账行不该产生，防御性指针）
	Status         string   // pending / approved / rejected
	CreatedAt      time.Time
	DecidedAt      *time.Time
	DecidedNote    string
	ComboID        *int64  // approve 写入的白名单行 id；NULL=无写入
	FlagWarn       *string // D-463 S2：warn 旗标命中透出（拼接 reason 短串；NULL=无旗）
}

// ListPaperForGraduation D-403 S2：毕业判定候选全集——active 行 + 近 14 天 exited 行
// （exit_ts ≥ now−14d；窗口裁剪在 SQL 单点收口，调用方免重复实现）。
func (s *Store) ListPaperForGraduation(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry
		WHERE status = 'active' OR (status = 'exited' AND exit_ts >= now() - interval '14 days')
		ORDER BY id`)
}

// InsertCarryGraduation 插毕业建议（paper_id UNIQUE 幂等：同 paper 二次扫描不重复建议）。
// 返回是否新插（false=已存在建议行，调用方不计 newCount）。
func (s *Store) InsertCarryGraduation(ctx context.Context, g *CarryGraduation) (bool, error) {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO carry_graduations (paper_id, venue_a, venue_b, venue_type_a, venue_type_b, symbol, structure, mt_venue, age_days, accrued_usd, last_funding_ann, net_all_in_usd, flag_warn)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (paper_id) DO NOTHING
		RETURNING id`,
		g.PaperID, g.VenueA, g.VenueB, g.VenueTypeA, g.VenueTypeB, g.Symbol, g.Structure, g.MtVenue, g.AgeDays, g.AccruedUSD, g.LastFundingAnn, g.NetAllInUSD, g.FlagWarn).Scan(&g.ID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("insert carry graduation: %w", err)
	}
	return true, nil
}

// GetCarryGraduation 单条建议（approve/reject 前置读取+pending 守卫）。
// VenuePair 配对展示串（venue_b 空→venue_a；否则 'a×b'）——与 venue 生成列同构，
// 语义键取 VenueA/VenueB 字段，禁回解析本串。
func (g CarryGraduation) VenuePair() string {
	if g.VenueB == "" {
		return g.VenueA
	}
	return g.VenueA + "×" + g.VenueB
}

func (s *Store) GetCarryGraduation(ctx context.Context, id int64) (*CarryGraduation, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, paper_id, venue, venue_a, venue_b, venue_type_a, venue_type_b, symbol, structure, mt_venue, age_days, accrued_usd,
		       last_funding_ann, net_all_in_usd, status, created_at, decided_at, decided_note, combo_id, flag_warn
		FROM carry_graduations WHERE id = $1`, id)
	return scanCarryGraduation(row)
}

// ListCarryGraduations 全量建议（前端毕业小节行源；created_at 倒序新建议在前）。
func (s *Store) ListCarryGraduations(ctx context.Context) ([]CarryGraduation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, paper_id, venue, venue_a, venue_b, venue_type_a, venue_type_b, symbol, structure, mt_venue, age_days, accrued_usd,
		       last_funding_ann, net_all_in_usd, status, created_at, decided_at, decided_note, combo_id, flag_warn
		FROM carry_graduations ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list carry graduations: %w", err)
	}
	defer rows.Close()
	var out []CarryGraduation
	for rows.Next() {
		var g CarryGraduation
		if err := rows.Scan(&g.ID, &g.PaperID, &g.Venue, &g.VenueA, &g.VenueB, &g.VenueTypeA, &g.VenueTypeB, &g.Symbol, &g.Structure, &g.MtVenue,
			&g.AgeDays, &g.AccruedUSD, &g.LastFundingAnn, &g.NetAllInUSD, &g.Status, &g.CreatedAt,
			&g.DecidedAt, &g.DecidedNote, &g.ComboID, &g.FlagWarn); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetPaperSpotCarryByID 纸面行单条（approve 需 paper 证据列与 venue 组合串）。
func (s *Store) GetPaperSpotCarryByID(ctx context.Context, id int64) (*PaperSpotCarry, error) {
	rows, err := s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, pgx.ErrNoRows
	}
	return &rows[0], nil
}

// UpdateCarryGraduationDecision 裁决落账：status/decided_at/combo_id/decided_note 单点
// （combo_id *int64：cfd approve=白名单行 id、非 cfd approve/reject=nil 即 NULL=无写入；
// pending 守卫在 WHERE——非 pending 零行更新由调用方转错误）。
func (s *Store) UpdateCarryGraduationDecision(ctx context.Context, id int64, status string, comboID *int64, note string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE carry_graduations SET status = $2, decided_at = now(), combo_id = $3, decided_note = $4
		WHERE id = $1 AND status = 'pending'`, id, status, comboID, note)
	if err != nil {
		return fmt.Errorf("update carry graduation %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("update carry graduation %d: not pending", id)
	}
	return nil
}

// XAssetComboInsert approve 写白名单行的入参（列=xasset_allow_combos 全可写列）。
type XAssetComboInsert struct {
	CanonicalBuy  string
	CanonicalSell string
	VenueBuy      string
	VenueSell     string
	MaxLongQty    float64
	MaxShortQty   float64
	MaxGrossUSD   float64
	NotionalUSD   *float64
	Simulated     bool
	Note          string
}

// InsertXAssetAllowCombo approve 写组合白名单（simulated=false=真仓白名单；venue env 门
// 仍是实际真钱闸，双层不矛盾）。撞唯一键 → ErrXAssetComboExists（调用方报「已在白名单」）。
func (s *Store) InsertXAssetAllowCombo(ctx context.Context, r *XAssetComboInsert) (int64, error) {
	var id int64
	vb, vs := keydomain.Parse(r.VenueBuy), keydomain.Parse(r.VenueSell) // D-496 复合键拆两列
	err := s.pool.QueryRow(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell,
			max_long_qty, max_short_qty, max_gross_usd, notional_usd, enabled, simulated, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true,$11,$12)
		RETURNING id`,
		r.CanonicalBuy, r.CanonicalSell, vb.Venue, vb.Type, vs.Venue, vs.Type,
		r.MaxLongQty, r.MaxShortQty, r.MaxGrossUSD, r.NotionalUSD, r.Simulated, r.Note).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, ErrXAssetComboExists
		}
		return 0, fmt.Errorf("insert xasset allow combo: %w", err)
	}
	return id, nil
}

// ListCfdCarryUniverseClean D-403 S4：detector hint 配对源——cfd×perp 结构且 note=”
// （D-345 suspect 疑因行拒入，fail-closed 不臆造配对）。
func (s *Store) ListCfdCarryUniverseClean(ctx context.Context) ([]CarryUniverseRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical, mt_venue, cex_venue, net_annual_pct, funding_annual_pct,
		       swap_long_pct, swap_short_pct, swap_free, structure, note
		FROM fin_carry_universe
		WHERE structure IN ('long_cfd_short_perp','short_cfd_long_perp') AND note = ''`)
	if err != nil {
		return nil, fmt.Errorf("list cfd carry universe: %w", err)
	}
	defer rows.Close()
	var out []CarryUniverseRow
	for rows.Next() {
		var r CarryUniverseRow
		if err := rows.Scan(&r.Canonical, &r.MtVenue, &r.CexVenue, &r.NetAnnualPct,
			&r.FundingAnnualPct, &r.SwapLongPct, &r.SwapShortPct, &r.SwapFree, &r.Structure, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanCarryGraduation(row pgx.Row) (*CarryGraduation, error) {
	var g CarryGraduation
	err := row.Scan(&g.ID, &g.PaperID, &g.Venue, &g.VenueA, &g.VenueB, &g.VenueTypeA, &g.VenueTypeB, &g.Symbol, &g.Structure, &g.MtVenue,
		&g.AgeDays, &g.AccruedUSD, &g.LastFundingAnn, &g.NetAllInUSD, &g.Status, &g.CreatedAt,
		&g.DecidedAt, &g.DecidedNote, &g.ComboID, &g.FlagWarn)
	if err != nil {
		return nil, err
	}
	return &g, nil
}
