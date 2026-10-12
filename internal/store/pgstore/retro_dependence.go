// retro_dependence.go —— D-180 复盘依赖性检验数据面：每策略按 settled_at 序的
// 净 PNL 符号序列（转折点检验输入；与 AggregateStrategyStats 同排除口径）。
package pgstore

import (
	"context"
	"fmt"
)

// retroStrategySequences 每策略平仓 net 序列（settled_at 升序）。
// 全量史非窗口——依赖性结构与周报窗口无关，样本越长检验越有意义（D-180 S②）。
func (s *Store) retroStrategySequences(ctx context.Context) (map[string][]float64, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT p.type, COALESCE(s.net_realized_usd, s.realized_usd)
		FROM position_settlements s
		JOIN positions p ON p.id = s.position_id
		WHERE p.closed_at IS NOT NULL AND p.realized_pnl IS NOT NULL
		  AND COALESCE(s.stats_status, 'applied') = 'applied' AND NOT %s AND %s
		ORDER BY p.type, s.settled_at`, incidentPositionPredicate, probeExclusionPredicate))
	if err != nil {
		return nil, fmt.Errorf("retro strategy sequences: %w", err)
	}
	defer rows.Close()
	out := map[string][]float64{}
	for rows.Next() {
		var t string
		var net float64
		if err := rows.Scan(&t, &net); err != nil {
			return nil, err
		}
		out[t] = append(out[t], net)
	}
	return out, rows.Err()
}
