// earn_yield.go —— D-524：earn 池收益增量归属读面（快照前值+已归属流窗算）。
// D-527：actual_apr 回写读面（earn 池申购本金+提案回链、仓位↔提案桥、池收益窗算）。
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

// EarnSubscribeSum (venue,ccy) 窗内 fin_dci_order opt_type='EARN' status='filled'
// 申购量合计——池增量归属的「已归属流入」扣减项（赎回无台账行=无扣减项，负 Δ 如实
// 负收益；划转台账现无 CEX earn 户记轨——禁臆造）。ccy 维度必带：扣减只对申购币种
// 自身行——venue 级汇总喂给逐币行=每行各扣一遍扇出假账（binance −$30k 实证，
// STATE「binance earn_yield 归属扇出修复」票）。
func (s *Store) EarnSubscribeSum(ctx context.Context, vn, ccy string, from, to time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(qty),0) FROM fin_dci_order
		WHERE venue=$1 AND opt_type='EARN' AND status='filled' AND invest_ccy=$2
		  AND created_at > $3 AND created_at <= $4`, vn, ccy, from, to).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("earn subscribe sum: %w", err)
	}
	return sum, nil
}

// EarnPoolOrder earn 池申购本金行（D-527 actual_apr 回写：提案回链+本金+申购时刻）。
type EarnPoolOrder struct {
	ProposalID int64
	Qty        float64
	CreatedAt  time.Time
}

// ListEarnPoolOrders (venue,investCcy) 下 status='filled' 且提案回链非空的 EARN 单。
// 池收益按申购时刻窗算，多提案多单按 qty 比例分摊收入（写方如实注记不分本金）。
func (s *Store) ListEarnPoolOrders(ctx context.Context, vn, ccy string) ([]EarnPoolOrder, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT proposal_id, qty, created_at FROM fin_dci_order
		WHERE venue=$1 AND opt_type='EARN' AND status='filled'
		  AND invest_ccy=$2 AND proposal_id IS NOT NULL
		ORDER BY created_at`, vn, ccy)
	if err != nil {
		return nil, fmt.Errorf("list earn pool orders: %w", err)
	}
	defer rows.Close()
	var out []EarnPoolOrder
	for rows.Next() {
		var o EarnPoolOrder
		if err := rows.Scan(&o.ProposalID, &o.Qty, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SumEarnYieldSince (venue,ccy) 池自 since 起 income_events earn_yield 收益净额
// （D-524③ 负 Δ 如实记负——赎出/划转台账无记轨）。零行=0 由调用方判「无收益不写」。
func (s *Store) SumEarnYieldSince(ctx context.Context, vn, ccy string, since time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_usd),0) FROM income_events
		WHERE venue=$1 AND venue_type='earn' AND symbol=$2
		  AND income_type='earn_yield' AND ts>=$3`, vn, ccy, since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("earn yield sum: %w", err)
	}
	return sum, nil
}
