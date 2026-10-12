package dashboard

import "context"

// alertInserter D-252：告警插入接口（pgstore.Store 满足；测试用 fakeAlertInserter）。
type alertInserter interface {
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}
