// paper_spot_carry_d042.go —— D-042 paper 双 CEX 轨 data_lost 冷却读口
// （paper_spot_carry 自身查，不加表；冷却/自盯两用）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CountDataLostExits D-042：24h 窗内 data_lost 退出行计数（venue_a×venue_b×symbol 对）——
// ≥2 → 24h 冷却不再 paper 开仓（paper_spot_carry 自身查，不加表）。
func (s *Store) CountDataLostExits(ctx context.Context, venueA, venueB, symbol string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM paper_spot_carry
		WHERE venue_a = $1 AND venue_b = $2 AND symbol = $3
		  AND status = 'exited' AND exit_reason = 'data_lost' AND exit_ts >= $4`,
		venueA, venueB, symbol, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count data_lost exits %s×%s|%s: %w", venueA, venueB, symbol, err)
	}
	return n, nil
}

// PaperDataLostStats D-042 R1 ③：24h 窗 data_lost 退出行数 + 冷却中对数（对=
// venue_a×venue_b×symbol 窗内 ≥2 次——与 scan 侧 dataLostCooldown 同口径，单查询双值）。
func (s *Store) PaperDataLostStats(ctx context.Context, since time.Time) (rows, cooldownPairs int, err error) {
	err = s.pool.QueryRow(ctx, `
		WITH w AS (
			SELECT venue_a, venue_b, symbol FROM paper_spot_carry
			WHERE status = 'exited' AND exit_reason = 'data_lost' AND exit_ts >= $1
		)
		SELECT (SELECT count(*) FROM w),
		       (SELECT count(*) FROM (SELECT venue_a, venue_b, symbol FROM w GROUP BY 1, 2, 3 HAVING count(*) >= 2) t)`,
		since).Scan(&rows, &cooldownPairs)
	if err != nil {
		return 0, 0, fmt.Errorf("paper data_lost stats: %w", err)
	}
	return rows, cooldownPairs, nil
}
