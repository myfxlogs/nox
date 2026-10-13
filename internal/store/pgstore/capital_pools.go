// capital_pools.go —— 资金地图读面（业主令 10-13 + D-285）：池快照 + 在跑归因三源
// （OPEN 持仓腿名义 / 未到期 DCI 申购 / earn 池全额——earn 池由快照 venue_type
// 自表，无须查询）。纯读面零写侧；幽灵池打标与 explorer_idle 同语义。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CapitalPoolRow 单 venue×venue_type 池最新快照行。
type CapitalPoolRow struct {
	Venue      string
	VenueType  string
	Equity     float64
	FreeMargin float64
	Currency   string
	Src        string
	FetchedAt  time.Time
	Ghost      bool // mt4/mt5 行且 venue 不在 broker_accounts（同 ExplorerIdlePool 语义）
}

// CapitalDeployedLeg OPEN 持仓腿在池名义归因（amount=成交量×成交均价，腿计价币）。
type CapitalDeployedLeg struct {
	VenueKey   string // 池键 venue:venue_type
	PositionID int64
	PosType    string // 策略族（positions.type）
	Amount     float64
}

// CapitalDCIOrder 未到期 DCI 申购行（status='filled' 且结算窗未过——与
// OutstandingDCIQty 同谓词口径，锁额归属 venue 现货池）。
type CapitalDCIOrder struct {
	ID    int64
	Venue string
	CCY   string
	Qty   float64
}

// CapitalPoolSnapshots fin_venue_account_snapshot 每池最新行（含 src 供 stale 判）。
func (s *Store) CapitalPoolSnapshots(ctx context.Context) ([]CapitalPoolRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type, currency)
			venue, venue_type, COALESCE(equity_raw,0), COALESCE(free_margin_raw,0),
			currency, COALESCE(src,''), fetched_at
		FROM fin_venue_account_snapshot
		ORDER BY venue, venue_type, currency, fetched_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("capital pool snapshots: %w", err)
	}
	defer rows.Close()
	var pools []CapitalPoolRow
	for rows.Next() {
		var p CapitalPoolRow
		if err := rows.Scan(&p.Venue, &p.VenueType, &p.Equity, &p.FreeMargin,
			&p.Currency, &p.Src, &p.FetchedAt); err != nil {
			return nil, fmt.Errorf("scan capital pool: %w", err)
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	vrows, err := s.pool.Query(ctx, `SELECT venue FROM broker_accounts`)
	if err != nil {
		return nil, fmt.Errorf("capital pool broker venues: %w", err)
	}
	for vrows.Next() {
		var v string
		if err := vrows.Scan(&v); err != nil {
			vrows.Close()
			return nil, fmt.Errorf("scan broker venue: %w", err)
		}
		registered[v] = true
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}
	for i := range pools {
		p := &pools[i]
		if p.VenueType == "mt4" || p.VenueType == "mt5" {
			p.Ghost = !registered[p.Venue]
		}
	}
	return pools, nil
}

// CapitalOpenLegs OPEN 持仓各 OPEN 腿在池名义：成交量×成交均价（无成交价回退
// price_actual；仍无=0 不猜价）。
func (s *Store) CapitalOpenLegs(ctx context.Context) ([]CapitalDeployedLeg, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.venue_key, p.id, COALESCE(p.type,''),
			COALESCE(l.filled_qty,0) * COALESCE(l.avg_fill_price, l.price_actual, 0)
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		WHERE p.status = 'OPEN' AND l.phase = 'OPEN'`)
	if err != nil {
		return nil, fmt.Errorf("capital open legs: %w", err)
	}
	defer rows.Close()
	var legs []CapitalDeployedLeg
	for rows.Next() {
		var l CapitalDeployedLeg
		if err := rows.Scan(&l.VenueKey, &l.PositionID, &l.PosType, &l.Amount); err != nil {
			return nil, fmt.Errorf("scan open leg: %w", err)
		}
		legs = append(legs, l)
	}
	return legs, rows.Err()
}

// CapitalOutstandingDCI 未到期 DCI 申购在外行（与 OutstandingDCIQty 同谓词：
// response.settleDate 回执优先，缺回退 created+tenor 估算窗）。
func (s *Store) CapitalOutstandingDCI(ctx context.Context) ([]CapitalDCIOrder, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, invest_ccy, qty FROM fin_dci_order
		WHERE status='filled'
		  AND CASE
		      WHEN response ~ '^\s*\{' AND (response::jsonb->>'settleDate') ~ '^\d{10,15}$'
		      THEN to_timestamp((response::jsonb->>'settleDate')::double precision/1000.0)
		      ELSE created_at + COALESCE(tenor_days,0) * interval '1 day'
		      END > now()`)
	if err != nil {
		return nil, fmt.Errorf("capital outstanding dci: %w", err)
	}
	defer rows.Close()
	var out []CapitalDCIOrder
	for rows.Next() {
		var o CapitalDCIOrder
		if err := rows.Scan(&o.ID, &o.Venue, &o.CCY, &o.Qty); err != nil {
			return nil, fmt.Errorf("scan dci order: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
