// margin_status.go —— D-405 S1/S6：fin_venue_margin_status 读写 + marginGuard 取数口。
// 表语义见 migration 0126：venue 主键判定结果小表，last_alert_at 落库使分档冷却跨重启可恢复。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// VenueMarginStatus marginGuard 每 venue 判定结果行（fin_venue_margin_status）。
type VenueMarginStatus struct {
	Venue             string
	VenueType         string
	Tier              string // normal / warn / urgent / data_stale
	CoverageDays      *float64
	DailyVolUSD       *float64
	SuggestedTopupUSD *float64
	LastAlertAt       *time.Time
	UpdatedAt         time.Time
}

// UpsertVenueMarginStatus marginGuard 每轮评估 upsert 判定结果（venue+venue_type 主键）。
// last_alert_at 由调用方决定（未告警保留旧值经参数传入）。
func (s *Store) UpsertVenueMarginStatus(ctx context.Context, r *VenueMarginStatus) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_venue_margin_status
			(venue, venue_type, tier, coverage_days, daily_vol_usd, suggested_topup_usd, last_alert_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (venue, venue_type) DO UPDATE SET
			tier=$3, coverage_days=$4, daily_vol_usd=$5, suggested_topup_usd=$6,
			last_alert_at=$7, updated_at=now()`,
		r.Venue, r.VenueType, r.Tier, r.CoverageDays, r.DailyVolUSD, r.SuggestedTopupUSD, r.LastAlertAt)
	return err
}

// GetVenueMarginStatus 读单 venue 判定行（冷却/去抖锚；无行 → ok=false）。
func (s *Store) GetVenueMarginStatus(ctx context.Context, venue, venueType string) (*VenueMarginStatus, bool, error) {
	var r VenueMarginStatus
	err := s.pool.QueryRow(ctx, `
		SELECT venue, venue_type, tier, coverage_days, daily_vol_usd, suggested_topup_usd, last_alert_at, updated_at
		FROM fin_venue_margin_status WHERE venue=$1 AND venue_type=$2`, venue, venueType).
		Scan(&r.Venue, &r.VenueType, &r.Tier, &r.CoverageDays, &r.DailyVolUSD, &r.SuggestedTopupUSD, &r.LastAlertAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get margin status %s/%s: %w", venue, venueType, err)
	}
	return &r, true, nil
}

// ListVenueMarginStatus 全量判定行（ListAccountMarginStatus RPC 数据源）。
func (s *Store) ListVenueMarginStatus(ctx context.Context) ([]VenueMarginStatus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, venue_type, tier, coverage_days, daily_vol_usd, suggested_topup_usd, last_alert_at, updated_at
		FROM fin_venue_margin_status ORDER BY venue, venue_type`)
	if err != nil {
		return nil, fmt.Errorf("list margin status: %w", err)
	}
	defer rows.Close()
	var out []VenueMarginStatus
	for rows.Next() {
		var r VenueMarginStatus
		if err := rows.Scan(&r.Venue, &r.VenueType, &r.Tier, &r.CoverageDays,
			&r.DailyVolUSD, &r.SuggestedTopupUSD, &r.LastAlertAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarginOpenLeg marginGuard 波动求和粒度：OPEN 仓 OPEN phase 腿 + canonical 锚。
// Qty/FilledQty 分域口径（D-434）：qty=请求口径（MT=手数 / CEX=合约张数），
// filled_qty=成交口径（MT=手数 / CEX=基础币）——单位折叠在消费侧（marginLegNotionalUSD），
// SQL 侧不再折叠（CEX filled 基础币 × ct_val perLot 曾致 ~1000x 低估）。
type MarginOpenLeg struct {
	PositionID int64
	OppID      string // positions.opportunity_id（canonical 由 combo.FromOppID 派生）
	Venue      string // leg.Venue=venueKey（"3001157:mt5"/"okx:perp"）
	Symbol     string // venue_symbol（ticks 查询键）
	Side       string
	Qty        float64  // l.qty 请求口径
	FilledQty  *float64 // l.filled_qty 成交口径（nil=未记录）
	Price      float64  // avg_fill_price → price_expected 回落
}

// ListOpenLegsForMargin 全部 OPEN 仓 OPEN phase 腿（marginGuard 波动求和输入）。
// price 折叠在 SQL 侧（COALESCE）；qty/filled_qty 两列原样出，折叠在消费侧（D-434）。
func (s *Store) ListOpenLegsForMargin(ctx context.Context) ([]MarginOpenLeg, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.position_id, COALESCE(p.opportunity_id,''), l.venue_key AS venue, l.symbol, l.side,
			l.qty, l.filled_qty,
			COALESCE(NULLIF(l.avg_fill_price,0), NULLIF(l.price_expected,0), 0)
		FROM position_legs l JOIN positions p ON p.id = l.position_id
		WHERE p.status='OPEN' AND l.phase='OPEN'`)
	if err != nil {
		return nil, fmt.Errorf("list open legs for margin: %w", err)
	}
	defer rows.Close()
	var out []MarginOpenLeg
	for rows.Next() {
		var l MarginOpenLeg
		if err := rows.Scan(&l.PositionID, &l.OppID, &l.Venue, &l.Symbol, &l.Side, &l.Qty, &l.FilledQty, &l.Price); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LatestVenueAccountSnapshot 单 (venue,venue_type) 最新快照行（开户门槛闸取数口）。
// 无行 → ok=false。
func (s *Store) LatestVenueAccountSnapshot(ctx context.Context, venue, venueType string) (*VenueAccountLatest, bool, error) {
	var r VenueAccountLatest
	err := s.pool.QueryRow(ctx, `
		SELECT venue, venue_type, balance_raw, equity_raw, free_margin_raw, buffer_multiple, currency, fetched_at
		FROM fin_venue_account_snapshot WHERE venue=$1 AND venue_type=$2
		ORDER BY fetched_at DESC LIMIT 1`, venue, venueType).
		Scan(&r.Venue, &r.VenueType, &r.BalanceRaw, &r.EquityRaw,
			&r.FreeMarginRaw, &r.BufferMultiple, &r.Currency, &r.FetchedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("latest venue snapshot %s/%s: %w", venue, venueType, err)
	}
	return &r, true, nil
}

// TickVolatility24h 腿 symbol 24h 价格半幅波动（D-405 波动源，标的价非价差）：
// vol = (max(bid)−min(bid))/2/mid，mid=(max+min)/2。
// ok=false = 无 ticks 或覆盖 <2h（min..max span < 2h）→ 调用方落 data_stale。
func (s *Store) TickVolatility24h(ctx context.Context, venue, venueType, symbol string) (vol float64, ok bool, err error) {
	var hi, lo *float64
	var first, last *time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT max(bid), min(bid), min(ts), max(ts) FROM ticks
		WHERE venue=$1 AND venue_type=$2 AND symbol=$3 AND ts > now() - interval '24 hours'`,
		venue, venueType, symbol).
		Scan(&hi, &lo, &first, &last)
	if err != nil {
		return 0, false, fmt.Errorf("tick vol %s/%s: %w", venue, symbol, err)
	}
	if hi == nil || lo == nil || first == nil || last == nil || *hi <= *lo {
		return 0, false, nil
	}
	if last.Sub(*first) < 2*time.Hour {
		return 0, false, nil // 覆盖 <2h → data_stale（SSOT §1）
	}
	mid := (*hi + *lo) / 2
	return (*hi - *lo) / 2 / mid, true, nil
}
