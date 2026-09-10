// D-270 S3：pgstore.Store → notify.RepoReplyStore 适配器。
package notify

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PgStoreReplyAdapter 包装 pgstore.Store 实现 notify.RepoReplyStore 接口。
type PgStoreReplyAdapter struct {
	Store *pgstore.Store
}

// FeishuTarget 透传。
func (a *PgStoreReplyAdapter) FeishuTarget(ctx context.Context) (chatID, openID string, err error) {
	return a.Store.FeishuTarget(ctx)
}

// GetPendingRepoOp 转换 pgstore.RepoOpRow → notify.RepoOpRow。
func (a *PgStoreReplyAdapter) GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error) {
	row, err := a.Store.GetPendingRepoOp(ctx, week)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &RepoOpRow{
		ID:             row.ID,
		ReminderCount:  row.ReminderCount,
		LastReminderAt: row.LastReminderAt,
		Status:         row.Status,
	}, nil
}

// ConfirmRepoOpOperated 透传。
func (a *PgStoreReplyAdapter) ConfirmRepoOpOperated(ctx context.Context, id int64, amountCny, ratePct float64, operatedDate time.Time, eventID, messageID, via string) (bool, error) {
	return a.Store.ConfirmRepoOpOperated(ctx, id, amountCny, ratePct, operatedDate, eventID, messageID, via)
}

// ConfirmRepoOpSkipped 透传。
func (a *PgStoreReplyAdapter) ConfirmRepoOpSkipped(ctx context.Context, id int64, reason, eventID, messageID, via string) (bool, error) {
	return a.Store.ConfirmRepoOpSkipped(ctx, id, reason, eventID, messageID, via)
}

// AddCNFIAction 透传。
func (a *PgStoreReplyAdapter) AddCNFIAction(ctx context.Context, code, layer, actionType string, qty, amountCny float64, navAtAction, ratePct *float64, signalSnapshot, rationale, actionDate string) (int64, error) {
	return a.Store.AddCNFIAction(ctx, code, layer, actionType, qty, amountCny, navAtAction, ratePct, signalSnapshot, rationale, actionDate)
}

// compile-time: *PgStoreReplyAdapter 实现 RepoReplyStore 接口。
var _ RepoReplyStore = (*PgStoreReplyAdapter)(nil)
