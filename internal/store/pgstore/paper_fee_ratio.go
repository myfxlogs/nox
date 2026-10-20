// paper_fee_ratio.go —— funding_carry fee 纸面轨绕行源（D-637 序③下游——
// PERP_FUNDING CLOSED 零行鸡生蛋，paper_spot_carry exited 估费行在案可绕）。
package pgstore

import (
	"context"
	"time"
)

// MedianPaperFeeRatio perp_perp exited 行 fee/qty 中位比+中位持有窗（小时）。
// 窗内零行/fee NULL→ok=false 缺证不外推；evidence 标 paper轨 口径（非 venue 账本）。
func (s *Store) MedianPaperFeeRatio(ctx context.Context, structure string, window time.Duration) (ratio, holdH float64, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY est_round_trip_fee_usd/qty_usd),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM exit_ts-entry_ts)/3600)
		FROM paper_spot_carry
		WHERE structure=$1 AND status='exited' AND est_round_trip_fee_usd IS NOT NULL
		  AND qty_usd > 0 AND exit_ts > now() - $2::interval`,
		structure, window).Scan(&ratio, &holdH)
	if err != nil {
		return 0, 0, false, err
	}
	return ratio, holdH, ratio > 0 && holdH > 0, nil
}
