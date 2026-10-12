package pgstore

import (
	"context"
	"fmt"
)

// AggregateStrategyStatsByEnvironment D-195-B：按 environment 过滤的 AggregateStrategyStats 变体。
// environment="" 时等价于 AggregateStrategyStats（不过滤）。
// 不改现有 AggregateStrategyStats 签名（settlement.go:35 接口定义不动）。
func (s *Store) AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (AggregateStat, error) {
	var st AggregateStat
	st.Strategy = strategy
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(total, 0), COALESCE(wins, 0), COALESCE(avg_pnl, 0), COALESCE(median_pnl, 0)
		FROM (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE net > 0) AS wins,
			       avg(net) AS avg_pnl,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY net) AS median_pnl
			FROM (
				SELECT COALESCE(s.net_realized_usd, s.realized_usd) AS net
				FROM position_settlements s
				JOIN positions p ON p.id = s.position_id
				WHERE p.type = $1 AND p.closed_at IS NOT NULL AND p.realized_pnl IS NOT NULL
				  AND COALESCE(s.stats_status, 'applied') = 'applied'
				  AND p.environment IS NOT NULL
				  AND ($2 = '' OR p.environment = $2)
				  AND NOT %s AND %s
			) t
		) agg`, incidentPositionPredicate, probeExclusionPredicate), strategy, environment).Scan(&st.Total, &st.Wins, &st.AvgPnl, &st.MedianPnl)
	if err != nil {
		return AggregateStat{}, fmt.Errorf("aggregate strategy stats by env: %w", err)
	}
	return st, nil
}

// DailyRealizedPnlByEnvironment D-195-B：按 environment 过滤的 DailyRealizedPnl 变体。
// D-193-A 扩包（决策 9）：与 DailyRealizedPnl 同构——LEFT JOIN + COALESCE 三级 fallback。
func (s *Store) DailyRealizedPnlByEnvironment(ctx context.Context, environment string) (float64, error) {
	var sum *float64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd, p.realized_pnl)), 0)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.status = 'CLOSED' AND p.closed_at >= date_trunc('day', now())
		  AND p.environment IS NOT NULL
		  AND ($1 = '' OR p.environment = $1)
		  AND NOT %s AND %s`, incidentPositionPredicate, probeExclusionPredicate), environment).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("daily realized pnl by env: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// AggregateStrategyStatsByEnvironmentFamily D-256-A S2：风险族聚合（多策略合并样本）。
// strategies 为同族策略名列表（如 CARRY/SWAPARB），用 p.type = ANY($1) 合并统计。
// environment="" 时等价于不过滤（与 AggregateStrategyStatsByEnvironment 语义一致）。
// 不改 AggregateStrategyStatsByEnvironment 签名（旧调用方不动）。
// 返回 AggregateStat.Strategy 取 strategies[0]（调用方已知族，字段仅供日志）。
func (s *Store) AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (AggregateStat, error) {
	var st AggregateStat
	if len(strategies) == 0 {
		return st, nil // 空族 → 零值（不查库）
	}
	st.Strategy = strategies[0]
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(total, 0), COALESCE(wins, 0), COALESCE(avg_pnl, 0), COALESCE(median_pnl, 0)
		FROM (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE net > 0) AS wins,
			       avg(net) AS avg_pnl,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY net) AS median_pnl
			FROM (
				SELECT COALESCE(s.net_realized_usd, s.realized_usd) AS net
				FROM position_settlements s
				JOIN positions p ON p.id = s.position_id
				WHERE p.type = ANY($1) AND p.closed_at IS NOT NULL AND p.realized_pnl IS NOT NULL
				  AND COALESCE(s.stats_status, 'applied') = 'applied'
				  AND ($2 = '' OR p.environment = $2)
				  AND NOT %s AND %s
			) t
		) agg`, incidentPositionPredicate, probeExclusionPredicate), strategies, environment).Scan(&st.Total, &st.Wins, &st.AvgPnl, &st.MedianPnl)
	if err != nil {
		return AggregateStat{}, fmt.Errorf("aggregate strategy stats by env family: %w", err)
	}
	return st, nil
}
