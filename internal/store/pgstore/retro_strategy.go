// retro_strategy.go —— 周报§3 策略行查询（D-181 扩 HPR 资本效率列，与
// AggregateStrategyStats 同排除口径：stats_status applied+排事故/探针仓）。
package pgstore

import (
	"context"
	"fmt"
)

// retroStrategyRows 周报§3 策略行：strategy_stats 累计面 + per-trade HPR 聚合副列。
// HPR=avg(net/notional_usd)——positions 无保证金列，名义本金为资本占用代理；
// notional 缺/0 的行不入 avg 但计入 HprAll（覆盖度=n_ok/n_all，口径缺如实标）。
func (s *Store) retroStrategyRows(ctx context.Context) ([]RetroStrategyRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT ss.strategy, ss.total_trades, ss.win_rate, ss.total_pnl, ss.disabled, ss.disabled_reason,
		       h.hpr, COALESCE(h.n_ok,0), COALESCE(h.n_all,0)
		FROM strategy_stats ss
		LEFT JOIN (
			SELECT p.type,
			       avg(COALESCE(s.net_realized_usd, s.realized_usd) / NULLIF(p.notional_usd,0)) AS hpr,
			       count(*) FILTER (WHERE p.notional_usd IS NOT NULL AND p.notional_usd > 0) AS n_ok,
			       count(*) AS n_all
			FROM position_settlements s
			JOIN positions p ON p.id = s.position_id
			WHERE p.closed_at IS NOT NULL AND p.realized_pnl IS NOT NULL
			  AND COALESCE(s.stats_status, 'applied') = 'applied' AND NOT %s AND %s
			GROUP BY p.type
		) h ON h.type = ss.strategy
		ORDER BY ss.total_pnl`, incidentPositionPredicate, probeExclusionPredicate))
	if err != nil {
		return nil, fmt.Errorf("retro strategy rows: %w", err)
	}
	defer rows.Close()
	var out []RetroStrategyRow
	for rows.Next() {
		var r RetroStrategyRow
		if err := rows.Scan(&r.Strategy, &r.Trades, &r.WinRate, &r.TotalPnl, &r.Disabled, &r.Reason,
			&r.HprAvg, &r.HprN, &r.HprAll); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
