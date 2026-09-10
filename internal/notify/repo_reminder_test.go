// D-270 S2 测试：周四操作提醒触发判定（fake store 注入，纯逻辑测试）。
package notify

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeReminderStore 注入测试数据，记录调用。
type fakeReminderStore struct {
	rates         []RepoRateRow
	avg30d        float64
	avgHas        bool
	isBusinessDay bool
	pending       *RepoOpRow
	created       bool
	incremented   bool
	alerts        []string // 记录 InsertAlert 的 message
	createErr     error
}

func (f *fakeReminderStore) LatestRepoRates(ctx context.Context) ([]RepoRateRow, error) {
	return f.rates, nil
}
func (f *fakeReminderStore) RepoRate30dAvg(ctx context.Context, code string) (float64, bool, error) {
	return f.avg30d, f.avgHas, nil
}
func (f *fakeReminderStore) CNFICalendarIsBusinessDay(ctx context.Context, day time.Time) (bool, error) {
	return f.isBusinessDay, nil
}
func (f *fakeReminderStore) CreateRepoOp(ctx context.Context, week, code, name string, ratePct, expectedReturn float64, holdingDays int, signalSnapshot string, triggerDate time.Time) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.created = true
	return 1, nil
}
func (f *fakeReminderStore) GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error) {
	return f.pending, nil
}
func (f *fakeReminderStore) IncrementRepoReminder(ctx context.Context, id int64) error {
	f.incremented = true
	return nil
}
func (f *fakeReminderStore) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	f.alerts = append(f.alerts, level+":"+message)
	return nil
}

// newReminderSvc 构造测试用提醒服务。
func newReminderSvc(t *testing.T, store *fakeReminderStore, now time.Time, warnPct, idleCny, minReturn float64, reminderHour int) *RepoReminderService {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	svc := NewRepoReminderService(store, func(level, msg string) {
		_ = store.InsertAlert(context.Background(), nil, level, msg)
	}, warnPct, idleCny, minReturn, reminderHour)
	svc.SetNowFn(func() time.Time { return now.In(loc) })
	return svc
}

// thursdayAt10 返回上海时区周四 10:00。
func thursdayAt10() time.Time {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return time.Date(2026, 9, 10, 10, 0, 0, 0, loc) // 2026-09-10 周四
}

// TestReminderTrigger_Thursday 周四+工作日+GC001 利率>阈值+idle>0 → CreateRepoOp + InsertAlert。
func TestReminderTrigger_Thursday(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", Name: "GC001", RatePct: 3.5}}, // 3.5 > 3.0 阈值
		avg30d:        2.50,
		avgHas:        true,
		isBusinessDay: true,
		pending:       nil, // 无 pending → 创建
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.created {
		t.Error("CreateRepoOp not called")
	}
	if len(store.alerts) == 0 {
		t.Fatal("no alert sent")
	}
	if store.alerts[0][:5] != "warn:" {
		t.Errorf("alert level = %q, want warn:", store.alerts[0][:5])
	}
}

// TestReminderTrigger_NotThursday 非周四/周五/周一 → 无操作。
func TestReminderTrigger_NotThursday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	for _, day := range []time.Time{
		time.Date(2026, 9, 8, 10, 0, 0, 0, loc),  // 周二
		time.Date(2026, 9, 9, 10, 0, 0, 0, loc),  // 周三
		time.Date(2026, 9, 13, 10, 0, 0, 0, loc), // 周日
	} {
		store := &fakeReminderStore{rates: []RepoRateRow{{Code: "204001", RatePct: 3.5}}, isBusinessDay: true}
		svc := newReminderSvc(t, store, day, 3.0, 50000, 5.0, 10)
		if err := svc.Check(context.Background()); err != nil {
			t.Fatalf("Check %s: %v", day.Weekday(), err)
		}
		if store.created || store.incremented || len(store.alerts) > 0 {
			t.Errorf("%s: unexpected action (created=%v incremented=%v alerts=%d)", day.Weekday(), store.created, store.incremented, len(store.alerts))
		}
	}
}

// TestReminderTrigger_Holiday 周四非工作日 → 无操作。
func TestReminderTrigger_Holiday(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: false, // 节假日
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called on holiday, want no action")
	}
}

// TestReminderTrigger_IdleZero idle=0 → 无操作。
func TestReminderTrigger_IdleZero(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 0, 5.0, 10) // idle=0
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called with idle=0, want no action")
	}
}

// TestReminderTrigger_RateBelowThreshold GC001 < warn 阈值 → 无操作。
func TestReminderTrigger_RateBelowThreshold(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 2.0}}, // 2.0 < 3.0
		isBusinessDay: true,
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called with rate below threshold, want no action")
	}
}

// TestReminderTrigger_AlreadyConfirmed 本周已 operated/skipped → 无操作（pending=nil，CreateRepoOp 唯一约束冲突→优雅跳过）。
func TestReminderTrigger_AlreadyConfirmed(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
		pending:       nil,                                       // 已确认 → 无 pending
		createErr:     errors.New("unique constraint violation"), // 模拟唯一约束冲突
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called when already confirmed, want no action")
	}
	if len(store.alerts) > 0 {
		t.Error("alert sent when already confirmed, want no action")
	}
}

// TestReminderTrigger_BeforeHour 未到提醒时段 → 无操作。
func TestReminderTrigger_BeforeHour(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	before10 := time.Date(2026, 9, 10, 9, 0, 0, 0, loc) // 周四 9:00 < 10
	store := &fakeReminderStore{rates: []RepoRateRow{{Code: "204001", RatePct: 3.5}}, isBusinessDay: true}
	svc := newReminderSvc(t, store, before10, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called before reminder hour, want no action")
	}
}

// TestReminderTrigger_ExpectedReturnBelowMin 预期收益 < 最小收益 → 无操作。
func TestReminderTrigger_ExpectedReturnBelowMin(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
	}
	// expectedReturn = 2.85 × 100 × 3 / 365 = 2.34 < 5 → 不触发
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 100, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.created {
		t.Error("CreateRepoOp called with expected return below min, want no action")
	}
}

// TestReminderTrigger_ThursdayPendingExists 周四有 pending 且今天未提醒 → IncrementRepoReminder + InsertAlert。
func TestReminderTrigger_ThursdayPendingExists(t *testing.T) {
	yesterday := thursdayAt10().Add(-24 * time.Hour)
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
		pending:       &RepoOpRow{ID: 1, ReminderCount: 1, LastReminderAt: &yesterday, Status: "pending"},
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.incremented {
		t.Error("IncrementRepoReminder not called")
	}
	if len(store.alerts) == 0 {
		t.Error("no alert sent")
	}
}

// TestReminderTrigger_ThursdayPendingAlreadyRemindedToday 周四有 pending 且今天已提醒 → 无操作。
func TestReminderTrigger_ThursdayPendingAlreadyRemindedToday(t *testing.T) {
	now := thursdayAt10()
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
		pending:       &RepoOpRow{ID: 1, ReminderCount: 1, LastReminderAt: &now, Status: "pending"}, // 今天已提醒
	}
	svc := newReminderSvc(t, store, now, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.incremented {
		t.Error("IncrementRepoReminder called but already reminded today")
	}
}

// TestReminderReRemind_Friday 周五仍 pending → IncrementRepoReminder + InsertAlert。
func TestReminderReRemind_Friday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 9, 11, 10, 0, 0, 0, loc) // 周五
	yesterday := thursdayAt10()
	store := &fakeReminderStore{
		isBusinessDay: true,
		pending:       &RepoOpRow{ID: 1, ReminderCount: 1, LastReminderAt: &yesterday, Status: "pending"},
	}
	svc := newReminderSvc(t, store, friday, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.incremented {
		t.Error("IncrementRepoReminder not called on Friday re-remind")
	}
	if len(store.alerts) == 0 {
		t.Error("no alert sent on Friday re-remind")
	}
}

// TestReminderReRemind_Monday 下周一仍 pending → IncrementRepoReminder + InsertAlert。
func TestReminderReRemind_Monday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	monday := time.Date(2026, 9, 14, 10, 0, 0, 0, loc) // 下周一
	lastWeek := time.Date(2026, 9, 11, 10, 0, 0, 0, loc)
	store := &fakeReminderStore{
		isBusinessDay: true,
		pending:       &RepoOpRow{ID: 1, ReminderCount: 2, LastReminderAt: &lastWeek, Status: "pending"},
	}
	svc := newReminderSvc(t, store, monday, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.incremented {
		t.Error("IncrementRepoReminder not called on Monday re-remind")
	}
}

// TestReminderReRemind_NotTargetDay 周二/周三/周日 → 不触发再提醒。
func TestReminderReRemind_NotTargetDay(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	for _, day := range []time.Time{
		time.Date(2026, 9, 8, 10, 0, 0, 0, loc),  // 周二
		time.Date(2026, 9, 9, 10, 0, 0, 0, loc),  // 周三
		time.Date(2026, 9, 13, 10, 0, 0, 0, loc), // 周日
	} {
		store := &fakeReminderStore{
			isBusinessDay: true,
			pending:       &RepoOpRow{ID: 1, ReminderCount: 1, Status: "pending"},
		}
		svc := newReminderSvc(t, store, day, 3.0, 50000, 5.0, 10)
		if err := svc.Check(context.Background()); err != nil {
			t.Fatalf("Check %s: %v", day.Weekday(), err)
		}
		if store.incremented || len(store.alerts) > 0 {
			t.Errorf("%s: unexpected re-remind (incremented=%v alerts=%d)", day.Weekday(), store.incremented, len(store.alerts))
		}
	}
}

// TestReminderReRemind_Holiday 周五非工作日 → 不触发再提醒。
func TestReminderReRemind_Holiday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 9, 11, 10, 0, 0, 0, loc)
	store := &fakeReminderStore{
		isBusinessDay: false, // 节假日
		pending:       &RepoOpRow{ID: 1, ReminderCount: 1, Status: "pending"},
	}
	svc := newReminderSvc(t, store, friday, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.incremented {
		t.Error("IncrementRepoReminder called on holiday, want no action")
	}
}

// TestReminderReRemind_NoPending 周五无 pending → 无操作。
func TestReminderReRemind_NoPending(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 9, 11, 10, 0, 0, 0, loc)
	store := &fakeReminderStore{
		isBusinessDay: true,
		pending:       nil, // 无 pending
	}
	svc := newReminderSvc(t, store, friday, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.incremented {
		t.Error("IncrementRepoReminder called with no pending, want no action")
	}
}

// TestReminderStopAfterConfirm 确认后不再提醒（pending=nil → 周五无操作）。
func TestReminderStopAfterConfirm(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 9, 11, 10, 0, 0, 0, loc)
	store := &fakeReminderStore{
		isBusinessDay: true,
		pending:       nil, // 已确认 → 无 pending
	}
	svc := newReminderSvc(t, store, friday, 3.0, 50000, 5.0, 10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.incremented || len(store.alerts) > 0 {
		t.Error("reminder fired after confirm, want no action")
	}
}

// TestReminderCreateError CreateRepoOp 失败（如唯一约束=本周已确认）→ Check 优雅降级返回 nil。
func TestReminderCreateError(t *testing.T) {
	store := &fakeReminderStore{
		rates:         []RepoRateRow{{Code: "204001", RatePct: 3.5}},
		isBusinessDay: true,
		pending:       nil,
		createErr:     errors.New("unique constraint violation"),
	}
	svc := newReminderSvc(t, store, thursdayAt10(), 3.0, 50000, 5.0, 10)
	err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check = %v, want nil (优雅降级：本周已确认或 DB 错误不崩溃)", err)
	}
	if len(store.alerts) > 0 {
		t.Error("alert sent on CreateRepoOp failure, want no action")
	}
}
