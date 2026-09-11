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

// GetRepoConfig R2：读 DB 配置，转换为 notify.RepoConfigRow。
func (a *PgStoreReminderAdapter) GetRepoConfig(ctx context.Context) (*RepoConfigRow, error) {
	c, err := a.Store.GetRepoConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &RepoConfigRow{
		IdleCny:      c.IdleCny,
		MinReturnCny: c.MinReturnCny,
		ReminderHour: c.ReminderHour,
		Enabled:      c.Enabled,
	}, nil
}

// RepoRate30dPercentile R2：透传分位数查询。
func (a *PgStoreReminderAdapter) RepoRate30dPercentile(ctx context.Context, code string, currentRate float64) (float64, bool, error) {
	return a.Store.RepoRate30dPercentile(ctx, code, currentRate)
}

// ListMaturedRepoTrades D-272：转换 pgstore.RepoTradeRow → notify.MaturedRepoTrade。
func (a *PgStoreReminderAdapter) ListMaturedRepoTrades(ctx context.Context, today time.Time) ([]MaturedRepoTrade, error) {
	rows, err := a.Store.ListMaturedRepoTrades(ctx, today)
	if err != nil {
		return nil, err
	}
	out := make([]MaturedRepoTrade, len(rows))
	for i, r := range rows {
		out[i] = MaturedRepoTrade{
			ID:             r.ID,
			InstrumentCode: r.InstrumentCode,
			PrincipalCny:   r.PrincipalCny,
		}
	}
	return out, nil
}

// UpdateMaturityReminded D-272：透传。
func (a *PgStoreReminderAdapter) UpdateMaturityReminded(ctx context.Context, id int64) error {
	return a.Store.UpdateMaturityReminded(ctx, id)
}

// compile-time: *PgStoreReminderAdapter 实现 RepoReminderStore 接口。
var _ RepoReminderStore = (*PgStoreReminderAdapter)(nil)
