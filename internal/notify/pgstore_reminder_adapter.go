// D-270 S2：pgstore.Store → notify.RepoReminderStore 适配器。
// 解耦：reminder_test 用 fake store，生产用 pgstore 适配器。
package notify

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PgStoreReminderAdapter 包装 pgstore.Store 实现 notify.RepoReminderStore 接口。
type PgStoreReminderAdapter struct {
	Store *pgstore.Store
}

// LatestRepoRates 转换 pgstore.RepoRate → notify.RepoRateRow。
func (a *PgStoreReminderAdapter) LatestRepoRates(ctx context.Context) ([]RepoRateRow, error) {
	rates, err := a.Store.LatestRepoRates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RepoRateRow, len(rates))
	for i, r := range rates {
		out[i] = RepoRateRow{Code: r.Code, Name: r.Name, RatePct: r.RatePct}
	}
	return out, nil
}

// RepoRate30dAvg 透传。
func (a *PgStoreReminderAdapter) RepoRate30dAvg(ctx context.Context, code string) (float64, bool, error) {
	return a.Store.RepoRate30dAvg(ctx, code)
}

// CNFICalendarIsBusinessDay 透传。
func (a *PgStoreReminderAdapter) CNFICalendarIsBusinessDay(ctx context.Context, day time.Time) (bool, error) {
	return a.Store.CNFICalendarIsBusinessDay(ctx, day)
}

// CreateRepoOp 透传。
func (a *PgStoreReminderAdapter) CreateRepoOp(ctx context.Context, week, code, name string, ratePct, expectedReturn float64, holdingDays int, signalSnapshot string, triggerDate time.Time) (int64, error) {
	return a.Store.CreateRepoOp(ctx, week, code, name, ratePct, expectedReturn, holdingDays, signalSnapshot, triggerDate)
}

// GetPendingRepoOp 转换 pgstore.RepoOpRow → notify.RepoOpRow。
func (a *PgStoreReminderAdapter) GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error) {
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

// IncrementRepoReminder 透传。
func (a *PgStoreReminderAdapter) IncrementRepoReminder(ctx context.Context, id int64) error {
	return a.Store.IncrementRepoReminder(ctx, id)
}

// InsertAlert 透传。
func (a *PgStoreReminderAdapter) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	return a.Store.InsertAlert(ctx, ruleID, level, message)
}

// compile-time: *PgStoreReminderAdapter 实现 RepoReminderStore 接口。
var _ RepoReminderStore = (*PgStoreReminderAdapter)(nil)
