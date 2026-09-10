// D-269 S4：pgstore.Store → trxvote.Store 适配器。
// 解耦：scheduler_test 用 fake store，生产用 pgstore 适配器。
package trxvote

import (
	"context"

	"arb/internal/store/pgstore"
)

// PgStoreAdapter 包装 pgstore.Store 实现 trxvote.Store 接口。
type PgStoreAdapter struct {
	Store *pgstore.Store
}

// InsertTrxVoteRebalance 转换 TRXVoteRebalanceRecord → pgstore.TRXVoteRebalance 并插入。
func (a *PgStoreAdapter) InsertTrxVoteRebalance(ctx context.Context, rec TRXVoteRebalanceRecord) (int64, error) {
	return a.Store.InsertTrxVoteRebalance(ctx, pgstore.TRXVoteRebalance{
		CurrentYield:   rec.CurrentYield,
		TargetYield:    rec.TargetYield,
		ImprovementPct: rec.ImprovementPct,
		NeedRebalance:  rec.NeedRebalance,
		Reason:         rec.Reason,
		TargetVotes:    rec.TargetVotes,
		Status:         rec.Status,
	})
}

// UpdateTrxVoteRebalanceResult 透传。
func (a *PgStoreAdapter) UpdateTrxVoteRebalanceResult(ctx context.Context, id int64, txHash, status string) error {
	return a.Store.UpdateTrxVoteRebalanceResult(ctx, id, txHash, status)
}

// TrxVoteRebalancedToday 透传。
func (a *PgStoreAdapter) TrxVoteRebalancedToday(ctx context.Context, maxPerDay int) bool {
	return a.Store.TrxVoteRebalancedToday(ctx, maxPerDay)
}

// compile-time: *PgStoreAdapter 实现 Store 接口。
var _ Store = (*PgStoreAdapter)(nil)
