package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nox/internal/data/keydomain"

	"github.com/jackc/pgx/v5"
)

// Opportunity 对应 opportunities 表（§2.6）。
type Opportunity struct {
	ID             string
	Type           string
	Canonical      string
	CanonicalSell  *string // D-300：卖腿 canonical（XASSET_CARRY；其余 type NULL）
	AssetClass     *string // D-300：标的等价类（XASSET_CARRY；其余 type NULL）
	VenueBuy       string
	VenueSell      string
	PriceBuy       float64
	PriceSell      float64
	GrossBps       float64
	SpreadCost     float64
	CommissionCost float64
	SlippageCost   float64
	SwapCost       float64
	NetBps         float64
	BreakevenDays  *float64
	QuoteTime      time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UpsertOpportunity 插入或刷新机会（D-080 S2：组合键=PK，同组合刷新同行）。
// 返回 inserted=true 表示本次是新插入（首次出现），false 表示刷新既有行（D-050 站内推送判定）。
// PostgreSQL UPSERT 技巧：RETURNING (xmax = 0) —— 本语句实际执行 INSERT（新行）时 xmax=0，
// 触发 ON CONFLICT DO UPDATE（刷新既有行）时 xmax≠0。
// id=组合键（6 段，无时间戳），同组合每 tick 刷新同行（last_seen=now()）。
func (s *Store) UpsertOpportunity(ctx context.Context, o *Opportunity) (bool, error) {
	q := `INSERT INTO opportunities (
			id, type, canonical, canonical_sell, asset_class, venue_buy, venue_type_buy, venue_sell, venue_type_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,now(),now())
		ON CONFLICT (id) DO UPDATE SET
			canonical=EXCLUDED.canonical,
			canonical_sell=EXCLUDED.canonical_sell, asset_class=EXCLUDED.asset_class,
			price_buy=EXCLUDED.price_buy, price_sell=EXCLUDED.price_sell,
			gross_bps=EXCLUDED.gross_bps, spread_cost=EXCLUDED.spread_cost,
			commission_cost=EXCLUDED.commission_cost, slippage_cost=EXCLUDED.slippage_cost,
			swap_cost=EXCLUDED.swap_cost, net_bps=EXCLUDED.net_bps,
			breakeven_days=EXCLUDED.breakeven_days, quote_time=EXCLUDED.quote_time,
			expires_at=EXCLUDED.expires_at, updated_at=now(), last_seen=now()
		RETURNING (xmax = 0) AS inserted`
	vb, vs := keydomain.Parse(o.VenueBuy), keydomain.Parse(o.VenueSell) // D-496 复合键拆两列
	var inserted bool
	if err := s.pool.QueryRow(ctx, q,
		o.ID, o.Type, o.Canonical, o.CanonicalSell, o.AssetClass, vb.Venue, vb.Type, vs.Venue, vs.Type, o.PriceBuy, o.PriceSell,
		o.GrossBps, o.SpreadCost, o.CommissionCost, o.SlippageCost, o.SwapCost,
		o.NetBps, o.BreakevenDays, o.QuoteTime, o.ExpiresAt).Scan(&inserted); err != nil {
		return false, fmt.Errorf("upsert opportunity: %w", err)
	}
	return inserted, nil
}

// ListOpportunities 返回未过期机会（§8.2）。
func (s *Store) ListOpportunities(ctx context.Context, typ string, limit, offset int) ([]Opportunity, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, type, canonical, canonical_sell, asset_class, venue_key_buy AS venue_buy, venue_key_sell AS venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		FROM opportunities
		WHERE expires_at > now()
		  AND ($1 = '' OR type = $1)
		ORDER BY net_bps DESC
		LIMIT $2 OFFSET $3`
	rows, err := s.pool.Query(ctx, q, typ, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list opportunities: %w", err)
	}
	defer rows.Close()
	return scanOpportunities(rows)
}

// GetOpportunity D-091 分析员 get_entity：按 ID 读单条机会（含已过期）。不存在→nil, nil。
func (s *Store) GetOpportunity(ctx context.Context, id string) (*Opportunity, error) {
	q := `SELECT id, type, canonical, canonical_sell, asset_class, venue_key_buy AS venue_buy, venue_key_sell AS venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		FROM opportunities WHERE id = $1`
	var o Opportunity
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&o.ID, &o.Type, &o.Canonical, &o.CanonicalSell, &o.AssetClass, &o.VenueBuy, &o.VenueSell, &o.PriceBuy, &o.PriceSell,
		&o.GrossBps, &o.SpreadCost, &o.CommissionCost, &o.SlippageCost, &o.SwapCost,
		&o.NetBps, &o.BreakevenDays, &o.QuoteTime, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get opportunity %s: %w", id, err)
	}
	return &o, nil
}

func scanOpportunities(rows pgx.Rows) ([]Opportunity, error) {
	var out []Opportunity
	for rows.Next() {
		var o Opportunity
		if err := rows.Scan(
			&o.ID, &o.Type, &o.Canonical, &o.CanonicalSell, &o.AssetClass, &o.VenueBuy, &o.VenueSell, &o.PriceBuy, &o.PriceSell,
			&o.GrossBps, &o.SpreadCost, &o.CommissionCost, &o.SlippageCost, &o.SwapCost,
			&o.NetBps, &o.BreakevenDays, &o.QuoteTime, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SettleAvg funding_history 结算均值（D-318 S1）：Avg=年化百分点均值（facts.value 库内口径
// 一致），N=参与期数（前端「N期/7d」展示）。
type SettleAvg struct {
	Avg float64
	N   int
}

// FundingSettleAvgs 按 (venue,symbol) 拉取 ts>=since 的 funding_history 结算均值（D-318 S1）。
// 返回 map[venue][symbol]SettleAvg；无行=无键（调用方按缺数据缺省）。
func (s *Store) FundingSettleAvgs(ctx context.Context, since time.Time) (map[string]map[string]SettleAvg, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT venue, symbol, avg(value), count(*) FROM facts WHERE kind='funding_history' AND ts >= $1 GROUP BY venue, symbol`,
		since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]map[string]SettleAvg)
	for rows.Next() {
		var venue, symbol string
		var sa SettleAvg
		if err := rows.Scan(&venue, &symbol, &sa.Avg, &sa.N); err != nil {
			return nil, err
		}
		if out[venue] == nil {
			out[venue] = make(map[string]SettleAvg)
		}
		out[venue][symbol] = sa
	}
	return out, rows.Err()
}
