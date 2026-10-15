// earn_yield.go —— D-524：earn 池收益增量归属读面（快照前值+已归属流窗算）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PrevSnapshotEquity (venue,venueType,ccy) 池行在 before 之前最近一行的 equity 值
// 与 fetched_at（增量归属基线读——新写行本身 fetched_at=new 天然排除）。
// 无行 → ok=false（首拍=基线化语义由调用方落地）。
func (s *Store) PrevSnapshotEquity(ctx context.Context, vn, venueType, ccy string, before time.Time) (float64, time.Time, bool, error) {
	var eq float64
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT equity_raw, fetched_at FROM fin_venue_account_snapshot
		WHERE venue=$1 AND venue_type=$2 AND currency=$3 AND fetched_at < $4
		ORDER BY fetched_at DESC LIMIT 1`, vn, venueType, ccy, before).Scan(&eq, &ts)
	if err == pgx.ErrNoRows {
		return 0, time.Time{}, false, nil
	}
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("prev snapshot equity: %w", err)
	}
	return eq, ts, true, nil
}

// EarnSubscribeSum (venue) 窗内 fin_dci_order opt_type='EARN' status='filled' 申购量
// 合计——池增量归属的「已归属流入」扣减项（赎回无台账行=无扣减项，负 Δ 如实负收益；
// 划转台账现无 CEX earn 户记轨——禁臆造）。
func (s *Store) EarnSubscribeSum(ctx context.Context, vn string, from, to time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty),0) FROM fin_dci_order
		WHERE venue=$1 AND opt_type='EARN' AND status='filled'
		  AND created_at > $2 AND created_at <= $3`, vn, from, to).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("earn subscribe sum: %w", err)
	}
	return sum, nil
}
