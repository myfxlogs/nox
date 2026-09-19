// D-270 S1 测试：cnfi_repo_ops Store 方法 + cnfi_actions.rate_pct + FeishuTarget + CNFICalendarIsBusinessDay（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// repoOpsTestStore 连接 ARB_TEST_PG_DSN 测试库，TRUNCATE cnfi_repo_ops + cnfi_actions + feishu_push_target 隔离数据。
func repoOpsTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE cnfi_repo_ops, cnfi_actions, alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// feishu_push_target 单行表：TRUNCATE CASCADE 会删迁移插入的 id=1 行，重新插入空行。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO feishu_push_target (id) VALUES (1) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("reseed feishu_push_target: %v", err)
	}
	return s
}

// TestRepoOp_CreateAndGetPending 插入 repo_op（含 signal_snapshot）→ GetPendingRepoOp 返回正确行。
func TestRepoOp_CreateAndGetPending(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) // 周四
	snap := `{"GC001":2.85,"avg30d":2.50,"warn":3.0}`
	id, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 2.85, 39.04, 3, snap, triggerDate)
	if err != nil {
		t.Fatalf("CreateRepoOp: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want > 0", id)
	}
	row, err := s.GetPendingRepoOp(ctx, "2026-W37")
	if err != nil {
		t.Fatalf("GetPendingRepoOp: %v", err)
	}
	if row == nil {
		t.Fatal("GetPendingRepoOp = nil, want row")
	}
	if row.InstrumentCode != "204001" {
		t.Errorf("InstrumentCode = %q, want 204001", row.InstrumentCode)
	}
	if row.RatePctAtTrigger != 2.85 {
		t.Errorf("RatePctAtTrigger = %v, want 2.85", row.RatePctAtTrigger)
	}
	if row.SignalSnapshot == nil || *row.SignalSnapshot != snap {
		t.Errorf("SignalSnapshot = %v, want %q", row.SignalSnapshot, snap)
	}
	if row.ReminderCount != 1 {
		t.Errorf("ReminderCount = %d, want 1", row.ReminderCount)
	}
	if row.Status != "pending" {
		t.Errorf("Status = %q, want pending", row.Status)
	}
}

// TestRepoOp_UniqueWeekCode 同周同品种重复创建 → 唯一约束 error。
func TestRepoOp_UniqueWeekCode(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if _, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 2.85, 39.04, 3, "{}", triggerDate); err != nil {
		t.Fatalf("first CreateRepoOp: %v", err)
	}
	if _, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 3.0, 41.0, 3, "{}", triggerDate); err == nil {
		t.Error("second CreateRepoOp same week+code: expected unique constraint error, got nil")
	}
}

// TestRepoOp_GetPending_None 无记录返回 (nil, nil)。
func TestRepoOp_GetPending_None(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	row, err := s.GetPendingRepoOp(ctx, "2026-W99")
	if err != nil {
		t.Fatalf("GetPendingRepoOp empty: %v", err)
	}
	if row != nil {
		t.Errorf("row = %v, want nil (no pending)", row)
	}
}

// TestRepoOp_IncrementReminder 更新提醒计数+时间。
func TestRepoOp_IncrementReminder(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	id, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 2.85, 39.04, 3, "{}", triggerDate)
	if err != nil {
		t.Fatalf("CreateRepoOp: %v", err)
	}
	if err := s.IncrementRepoReminder(ctx, id); err != nil {
		t.Fatalf("IncrementRepoReminder: %v", err)
	}
	row, err := s.GetPendingRepoOp(ctx, "2026-W37")
	if err != nil {
		t.Fatalf("GetPendingRepoOp: %v", err)
	}
	if row.ReminderCount != 2 {
		t.Errorf("ReminderCount = %d, want 2", row.ReminderCount)
	}
	if row.LastReminderAt == nil {
		t.Error("LastReminderAt = nil, want non-nil after IncrementRepoReminder")
	}
}

// TestRepoOp_ConfirmOperated 第一次返回 (true, nil)；第二次（同 event_id）返回 (false, nil)（幂等）。
func TestRepoOp_ConfirmOperated(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	id, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 2.85, 39.04, 3, "{}", triggerDate)
	if err != nil {
		t.Fatalf("CreateRepoOp: %v", err)
	}
	operatedDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	ok, err := s.ConfirmRepoOpOperated(ctx, id, 50000, 2.85, operatedDate, "evt-1", "msg-1", "feishu")
	if err != nil {
		t.Fatalf("first ConfirmRepoOpOperated: %v", err)
	}
	if !ok {
		t.Error("first ConfirmRepoOpOperated = false, want true")
	}
	// 第二次（同 event_id 已写入，feishu_event_id IS NULL 不再匹配）→ 幂等 false
	ok2, err := s.ConfirmRepoOpOperated(ctx, id, 50000, 2.85, operatedDate, "evt-2", "msg-2", "feishu")
	if err != nil {
		t.Fatalf("second ConfirmRepoOpOperated: %v", err)
	}
	if ok2 {
		t.Error("second ConfirmRepoOpOperated = true, want false (idempotent)")
	}
}

// TestRepoOp_ConfirmSkipped 同上幂等。
func TestRepoOp_ConfirmSkipped(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	id, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 2.85, 39.04, 3, "{}", triggerDate)
	if err != nil {
		t.Fatalf("CreateRepoOp: %v", err)
	}
	ok, err := s.ConfirmRepoOpSkipped(ctx, id, "周末不在家", "evt-1", "msg-1", "feishu")
	if err != nil {
		t.Fatalf("first ConfirmRepoOpSkipped: %v", err)
	}
	if !ok {
		t.Error("first ConfirmRepoOpSkipped = false, want true")
	}
	ok2, err := s.ConfirmRepoOpSkipped(ctx, id, "周末不在家", "evt-2", "msg-2", "feishu")
	if err != nil {
		t.Fatalf("second ConfirmRepoOpSkipped: %v", err)
	}
	if ok2 {
		t.Error("second ConfirmRepoOpSkipped = true, want false (idempotent)")
	}
}

// TestRepoOp_ListRepoOps 列出操作历史（按 id DESC）。
func TestRepoOp_ListRepoOps(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for _, week := range []string{"2026-W37", "2026-W38", "2026-W39"} {
		if _, err := s.CreateRepoOp(ctx, week, "204001", "GC001", 2.85, 39.04, 3, "{}", triggerDate); err != nil {
			t.Fatalf("CreateRepoOp %s: %v", week, err)
		}
	}
	rows, err := s.ListRepoOps(ctx, 10)
	if err != nil {
		t.Fatalf("ListRepoOps: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
	// 最新插入的 W39 应排第一（ORDER BY id DESC）
	if rows[0].OpportunityWeek != "2026-W39" {
		t.Errorf("rows[0].OpportunityWeek = %q, want 2026-W39", rows[0].OpportunityWeek)
	}
}

// TestAddCNFIAction_RatePct ratePct 非空 → cnfi_actions.rate_pct 存值。
func TestAddCNFIAction_RatePct(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	rate := 2.85
	id, err := s.AddCNFIAction(ctx, "GC001", "liquidity", "buy", 0, 50000, nil, &rate, "{}", "逆回购操作", "2026-09-10")
	if err != nil {
		t.Fatalf("AddCNFIAction: %v", err)
	}
	var dbRate *float64
	if err := s.pool.QueryRow(ctx, `SELECT rate_pct FROM cnfi_actions WHERE id=$1`, id).Scan(&dbRate); err != nil {
		t.Fatalf("query rate_pct: %v", err)
	}
	if dbRate == nil || *dbRate != 2.85 {
		t.Errorf("rate_pct = %v, want 2.85", dbRate)
	}
}

// TestAddCNFIAction_RatePctNil ratePct=nil → cnfi_actions.rate_pct NULL（向后兼容）。
func TestAddCNFIAction_RatePctNil(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	id, err := s.AddCNFIAction(ctx, "511010", "liquidity", "buy", 10, 5000, nil, nil, "{}", "买入货基", "2026-09-10")
	if err != nil {
		t.Fatalf("AddCNFIAction: %v", err)
	}
	var dbRate *float64
	if err := s.pool.QueryRow(ctx, `SELECT rate_pct FROM cnfi_actions WHERE id=$1`, id).Scan(&dbRate); err != nil {
		t.Fatalf("query rate_pct: %v", err)
	}
	if dbRate != nil {
		t.Errorf("rate_pct = %v, want nil (向后兼容)", dbRate)
	}
}

// TestFeishuTarget 返回 chat_id+open_id。
func TestFeishuTarget(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	if err := s.UpsertFeishuTarget(ctx, "oc_test_chat", "ou_test_open"); err != nil {
		t.Fatalf("UpsertFeishuTarget: %v", err)
	}
	chatID, openID, err := s.FeishuTarget(ctx)
	if err != nil {
		t.Fatalf("FeishuTarget: %v", err)
	}
	if chatID != "oc_test_chat" {
		t.Errorf("chatID = %q, want oc_test_chat", chatID)
	}
	if openID != "ou_test_open" {
		t.Errorf("openID = %q, want ou_test_open", openID)
	}
	// 清理：重置 feishu_push_target 为空行，避免污染 TestFeishuTargetRoundTrip（共享单行表）。
	if _, err := s.pool.Exec(ctx, `UPDATE feishu_push_target SET chat_id='', open_id='' WHERE id=1`); err != nil {
		t.Fatalf("reset feishu_push_target: %v", err)
	}
}

// TestCNFICalendarIsBusinessDay 工作日=true；节假日=false；日历缺失=false。
func TestCNFICalendarIsBusinessDay(t *testing.T) {
	s := repoOpsTestStore(t)
	ctx := context.Background()
	// 插入日历：2026-09-10 周四工作日，2026-09-12 周六非工作日
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_calendar (cal_date, is_business_day, cal_version) VALUES
		('2026-09-10', true, 'test'),
		('2026-09-12', false, 'test')`); err != nil {
		t.Fatalf("seed calendar: %v", err)
	}
	// 工作日
	biz, err := s.CNFICalendarIsBusinessDay(ctx, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IsBusinessDay 9-10: %v", err)
	}
	if !biz {
		t.Error("9-10 = false, want true (工作日)")
	}
	// 节假日
	biz, err = s.CNFICalendarIsBusinessDay(ctx, time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IsBusinessDay 9-12: %v", err)
	}
	if biz {
		t.Error("9-12 = true, want false (节假日)")
	}
	// 日历缺失=保守 false
	biz, err = s.CNFICalendarIsBusinessDay(ctx, time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("IsBusinessDay 12-25: %v", err)
	}
	if biz {
		t.Error("12-25 = true, want false (日历缺失=保守 false)")
	}
}
