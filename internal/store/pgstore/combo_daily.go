package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ComboDailyRow combo_daily_rollup 表行（D-080 S4）。
type ComboDailyRow struct {
	ComboKey  string
	Day       time.Time // UTC 日期
	Type      string
	Samples   int
	NetAvg    float64
	NetMin    float64
	NetMax    float64
	AnnualAvg *float64 // FUNDING_HEDGE/PERP_FUNDING=net_avg，其他=NULL
	FirstAt   time.Time
	LastAt    time.Time
}

// UpsertComboDaily 批量 upsert combo 日度 rollup（D-080 S4）。
// 一条多行 INSERT ... ON CONFLICT (combo_key, day) DO UPDATE（覆盖语义）。
// 覆盖范围=本 loop 本轮 dirty 集；并发多 loop 同日同组合覆盖=最后一次 flush 即最终态。
func (s *Store) UpsertComboDaily(ctx context.Context, rows []ComboDailyRow) error {
	if len(rows) == 0 {
		return nil
	}
	const fieldsPerRow = 10
	var b strings.Builder
	b.WriteString(`INSERT INTO combo_daily_rollup
		(combo_key, day, type, samples, net_avg, net_min, net_max, annual_avg, first_at, last_at)
		VALUES `)
	args := make([]any, 0, len(rows)*fieldsPerRow)
	for i, r := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		base := i * fieldsPerRow
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10)
		args = append(args, r.ComboKey, r.Day, r.Type, r.Samples, r.NetAvg, r.NetMin, r.NetMax, r.AnnualAvg, r.FirstAt, r.LastAt)
	}
	b.WriteString(` ON CONFLICT (combo_key, day) DO UPDATE SET
		samples=EXCLUDED.samples, net_avg=EXCLUDED.net_avg, net_min=EXCLUDED.net_min,
		net_max=EXCLUDED.net_max, annual_avg=EXCLUDED.annual_avg,
		first_at=EXCLUDED.first_at, last_at=EXCLUDED.last_at`)
	_, err := s.pool.Exec(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("upsert combo daily: %w", err)
	}
	return nil
}

// ListComboDaily 查询指定组合键的日度 rollup（D-080 S5 对账报表用）。
// 按 day DESC 排序（limit=1 取最新；limit=0=365 全量倒序，对账报表用）。
func (s *Store) ListComboDaily(ctx context.Context, comboKey string, limit int) ([]ComboDailyRow, error) {
	if limit <= 0 {
		limit = 365
	}
	q := `SELECT combo_key, day, type, samples, net_avg, net_min, net_max, annual_avg, first_at, last_at
		FROM combo_daily_rollup
		WHERE combo_key = $1
		ORDER BY day DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, comboKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list combo daily: %w", err)
	}
	defer rows.Close()
	var out []ComboDailyRow
	for rows.Next() {
		var r ComboDailyRow
		if err := rows.Scan(&r.ComboKey, &r.Day, &r.Type, &r.Samples, &r.NetAvg, &r.NetMin, &r.NetMax, &r.AnnualAvg, &r.FirstAt, &r.LastAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StrategyAgg 策略聚合行（D-080 S5：理论 vs 实际对账）。
type StrategyAgg struct {
	Strategy          string
	TotalPositions    int
	OpenPositions     int
	PromisedAnnualPct *float64 // 活跃持仓 expected_annual_pct 均值（NULL=全 CLOSED 或收敛型）
	RealizedPnlUsd    float64  // CLOSED 持仓 SUM(realized_pnl)
	AvgHoldDays       float64  // CLOSED 持仓 (closed_at-opened_at) 均值（天）
}

// GetStrategyAgg D-080 S5：按策略聚合持仓数据（理论 vs 实际对账底座）。
// strategy="" → 全策略；指定 → 单策略。
// actual_annual_pct 由 service 层用 notional 估算（store 不知 notional）。
func (s *Store) GetStrategyAgg(ctx context.Context, strategy string) ([]StrategyAgg, error) {
	q := `SELECT
		type AS strategy,
		COUNT(*)::int AS total_positions,
		COUNT(*) FILTER (WHERE status IN ('OPENING','OPEN','LEGGED','CLOSING'))::int AS open_positions,
		AVG(expected_annual_pct) FILTER (WHERE status IN ('OPENING','OPEN','LEGGED','CLOSING') AND expected_annual_pct IS NOT NULL) AS promised_annual_pct,
		COALESCE(SUM(realized_pnl) FILTER (WHERE status = 'CLOSED'), 0) AS realized_pnl_usd,
		COALESCE(AVG(EXTRACT(EPOCH FROM (closed_at - opened_at)) / 86400) FILTER (WHERE status = 'CLOSED' AND closed_at IS NOT NULL AND opened_at IS NOT NULL), 0) AS avg_hold_days
	FROM positions
	WHERE ($1 = '' OR type = $1)
	GROUP BY type
	ORDER BY type`
	rows, err := s.pool.Query(ctx, q, strategy)
	if err != nil {
		return nil, fmt.Errorf("get strategy agg: %w", err)
	}
	defer rows.Close()
	var out []StrategyAgg
	for rows.Next() {
		var a StrategyAgg
		if err := rows.Scan(&a.Strategy, &a.TotalPositions, &a.OpenPositions, &a.PromisedAnnualPct, &a.RealizedPnlUsd, &a.AvgHoldDays); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
