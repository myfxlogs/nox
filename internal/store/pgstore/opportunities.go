package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Opportunity 对应 opportunities 表（§2.6）。
type Opportunity struct {
	ID              string
	Type            string
	VenueBuy        string
	VenueSell       string
	PriceBuy        float64
	PriceSell       float64
	GrossBps        float64
	SpreadCost      float64
	CommissionCost  float64
	SlippageCost    float64
	SwapCost        float64
	NetBps          float64
	BreakevenDays   *float64
	QuoteTime       time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// UpsertOpportunity 插入或刷新机会（§5.0 去重：同键未过期 → 刷新数值）。
func (s *Store) UpsertOpportunity(ctx context.Context, o *Opportunity) error {
	q := `INSERT INTO opportunities (
			id, type, venue_buy, venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,now(),now())
		ON CONFLICT (id) DO UPDATE SET
			price_buy=EXCLUDED.price_buy, price_sell=EXCLUDED.price_sell,
			gross_bps=EXCLUDED.gross_bps, spread_cost=EXCLUDED.spread_cost,
			commission_cost=EXCLUDED.commission_cost, slippage_cost=EXCLUDED.slippage_cost,
			swap_cost=EXCLUDED.swap_cost, net_bps=EXCLUDED.net_bps,
			breakeven_days=EXCLUDED.breakeven_days, quote_time=EXCLUDED.quote_time,
			expires_at=EXCLUDED.expires_at, updated_at=now()`
	_, err := s.pool.Exec(ctx, q,
		o.ID, o.Type, o.VenueBuy, o.VenueSell, o.PriceBuy, o.PriceSell,
		o.GrossBps, o.SpreadCost, o.CommissionCost, o.SlippageCost, o.SwapCost,
		o.NetBps, o.BreakevenDays, o.QuoteTime, o.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert opportunity: %w", err)
	}
	return nil
}

// ListOpportunities 返回未过期机会（§8.2）。
func (s *Store) ListOpportunities(ctx context.Context, typ string, limit, offset int) ([]Opportunity, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, type, venue_buy, venue_sell, price_buy, price_sell,
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

// Alert 对应 alerts 表（§2.5）。
type Alert struct {
	ID        int64
	RuleID    *int64
	RuleName  string
	Ts        time.Time
	Level     string
	Message   string
	Acked     bool
}

// InsertAlert 写入告警（§2.5）。
func (s *Store) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	q := `INSERT INTO alerts (rule_id, level, message) VALUES ($1, $2, $3)`
	_, err := s.pool.Exec(ctx, q, ruleID, level, message)
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

// ListAlerts 分页返回告警（§8.2）。
func (s *Store) ListAlerts(ctx context.Context, limit, offset int) ([]Alert, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT a.id, a.rule_id, COALESCE(r.name, ''), a.ts, a.level, a.message, a.acked
		FROM alerts a LEFT JOIN rules r ON a.rule_id = r.id
		ORDER BY a.ts DESC LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckAlert 确认单条告警（幂等）。
func (s *Store) AckAlert(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE id = $1`, id)
	return err
}

// AckAll 确认全部未读告警。
func (s *Store) AckAll(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE acked = false`)
	return err
}

// ListUnacked 返回未读告警 + 计数。
func (s *Store) ListUnacked(ctx context.Context) ([]Alert, int, error) {
	q := `SELECT a.id, a.rule_id, COALESCE(r.name, ''), a.ts, a.level, a.message, a.acked
		FROM alerts a LEFT JOIN rules r ON a.rule_id = r.id
		WHERE a.acked = false ORDER BY a.ts DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("list unacked: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}

func scanOpportunities(rows pgx.Rows) ([]Opportunity, error) {
	var out []Opportunity
	for rows.Next() {
		var o Opportunity
		if err := rows.Scan(
			&o.ID, &o.Type, &o.VenueBuy, &o.VenueSell, &o.PriceBuy, &o.PriceSell,
			&o.GrossBps, &o.SpreadCost, &o.CommissionCost, &o.SlippageCost, &o.SwapCost,
			&o.NetBps, &o.BreakevenDays, &o.QuoteTime, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
