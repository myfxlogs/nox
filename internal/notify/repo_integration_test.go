// D-270 S5 集成测试：repo rate 插入 → 提醒 → repo op → 飞书回复 → 确认 → CN-FI action（DSN 真实夹具）。
package notify

import (
	"context"
	"os"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// integrationTestStore 连接 ARB_TEST_PG_DSN 测试库，迁移 schema，TRUNCATE 隔离数据，绑定飞书推送目标。
func integrationTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	// 迁移 schema（确保 0080_cnfi_repo_ops 等表存在）。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		TRUNCATE cnfi_repo_ops, cnfi_actions, alerts, fin_repo_rates, cnfi_calendar CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// feishu_push_target 单行表：TRUNCATE CASCADE 会删迁移插入的 id=1 行，重新插入并绑定。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO feishu_push_target (id, chat_id, open_id)
		VALUES (1, 'oc_integration', 'ou_owner')
		ON CONFLICT (id) DO UPDATE SET chat_id='oc_integration', open_id='ou_owner'`); err != nil {
		t.Fatalf("reseed feishu_push_target: %v", err)
	}
	// 插入周四（2026-09-10）为工作日。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO cnfi_calendar (cal_date, is_business_day, cal_version) VALUES ('2026-09-10', true, 'test')`); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return s
}

// insertTestRepoRates 插入 GC001 利率快照（高于阈值）。
// 历史利率先插入（ts 较早），最新利率后插入（ts 最晚），确保 LatestRepoRates 查询返回最新值。
func insertTestRepoRates(t *testing.T, s *pgstore.Store, ratePct float64) {
	t.Helper()
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// 先插入 30 条历史利率（ts 较早）。
	for i := 30; i >= 1; i-- {
		d := tradeDate.AddDate(0, 0, -i)
		if err := s.InsertRepoRate(ctx, "204001", "GC001", 2.50, nil, d); err != nil {
			t.Fatalf("InsertRepoRate history %d: %v", i, err)
		}
	}
	// 最后插入最新利率（ts 最晚，LatestRepoRates 查询 ORDER BY ts DESC 返回此条）。
	if err := s.InsertRepoRate(ctx, "204001", "GC001", ratePct, nil, tradeDate); err != nil {
		t.Fatalf("InsertRepoRate GC001: %v", err)
	}
}

// thursdayAt10 已在 repo_reminder_test.go 定义，此处复用。

// setTestRepoConfig 设置 DB 配置（idle=50000, minReturn=5, hour=10, enabled=true）。
func setTestRepoConfig(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if err := s.SetRepoConfig(context.Background(), 50000, 5, 10, true); err != nil {
		t.Fatalf("SetRepoConfig: %v", err)
	}
}

// TestIntegration_OKFlow repo rate 插入 → 提醒 → repo op → OK 回复 → 确认 operated + cnfi_actions。
func TestIntegration_OKFlow(t *testing.T) {
	s := integrationTestStore(t)
	ctx := context.Background()
	insertTestRepoRates(t, s, 3.5) // 3.5 > 0.2 活期基准
	setTestRepoConfig(t, s)

	// 1. 提醒服务触发 → 创建 pending repo op。
	reminderStore := &PgStoreReminderAdapter{Store: s}
	var alertSent bool
	svc := NewRepoReminderService(reminderStore, func(level, msg string) {
		alertSent = true
		_ = s.InsertAlert(ctx, nil, level, msg)
	})
	svc.SetNowFn(thursdayAt10)
	if err := svc.Check(ctx); err != nil {
		t.Fatalf("reminder Check: %v", err)
	}
	if !alertSent {
		t.Fatal("reminder did not send alert")
	}

	// 验证 pending repo op 已创建。
	week := isoWeek(thursdayAt10())
	pending, err := s.GetPendingRepoOp(ctx, week)
	if err != nil {
		t.Fatalf("GetPendingRepoOp: %v", err)
	}
	if pending == nil {
		t.Fatal("pending repo op not created")
	}
	if pending.Status != "pending" {
		t.Errorf("status = %q, want pending", pending.Status)
	}

	// 2. 飞书回复 OK → 确认 operated + 写 cnfi_actions。
	replyStore := &PgStoreReplyAdapter{Store: s}
	handler := NewRepoReplyHandler(replyStore)
	handler.SetNowFn(thursdayAt10)
	reply, err := handler.HandleReply(ctx, ReplyEvent{
		EventID:      "evt-ok-flow",
		MessageID:    "msg-ok-flow",
		ChatID:       "oc_integration",
		SenderOpenID: "ou_owner",
		MessageType:  "text",
		Content:      `{"text":"OK GC001 50000 2.85"}`,
	})
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if reply == "" {
		t.Error("reply empty, want confirmation text")
	}

	// 3. 验证 repo op 已确认 operated。
	confirmed, err := s.GetPendingRepoOp(ctx, week)
	if err != nil {
		t.Fatalf("GetPendingRepoOp after confirm: %v", err)
	}
	if confirmed != nil {
		t.Error("pending repo op still exists after confirm, want nil (status=operated)")
	}

	// 4. 验证 cnfi_actions 已写入（layer=liquidity, rate_pct=2.85）。
	actions, err := s.ListCNFIActions(ctx, 10)
	if err != nil {
		t.Fatalf("ListCNFIActions: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions count = %d, want 1", len(actions))
	}
	a := actions[0]
	if a.Layer != "liquidity" {
		t.Errorf("layer = %q, want liquidity", a.Layer)
	}
	if a.Code != "204001" {
		t.Errorf("code = %q, want 204001", a.Code)
	}
	if a.RatePct == nil || *a.RatePct != 2.85 {
		t.Errorf("rate_pct = %v, want 2.85", a.RatePct)
	}
	if a.AmountCny != 50000 {
		t.Errorf("amount_cny = %v, want 50000", a.AmountCny)
	}

	// 5. 验证 feishu_event_id/feishu_message_id 已写入（审计链）。
	var evID, msgID string
	err = s.Pool().QueryRow(ctx,
		`SELECT feishu_event_id, feishu_message_id FROM cnfi_repo_ops WHERE opportunity_week = $1`,
		week).Scan(&evID, &msgID)
	if err != nil {
		t.Fatalf("query event_id/message_id: %v", err)
	}
	if evID != "evt-ok-flow" {
		t.Errorf("feishu_event_id = %q, want evt-ok-flow", evID)
	}
	if msgID != "msg-ok-flow" {
		t.Errorf("feishu_message_id = %q, want msg-ok-flow", msgID)
	}
}

// TestIntegration_SKIPFlow repo rate 插入 → 提醒 → repo op → SKIP 回复 → 确认 skipped 无 cnfi_actions。
func TestIntegration_SKIPFlow(t *testing.T) {
	s := integrationTestStore(t)
	ctx := context.Background()
	insertTestRepoRates(t, s, 3.5)
	setTestRepoConfig(t, s)

	// 1. 提醒服务触发 → 创建 pending repo op。
	reminderStore := &PgStoreReminderAdapter{Store: s}
	var alertSent bool
	svc := NewRepoReminderService(reminderStore, func(level, msg string) {
		alertSent = true
		_ = s.InsertAlert(ctx, nil, level, msg)
	})
	svc.SetNowFn(thursdayAt10)
	if err := svc.Check(ctx); err != nil {
		t.Fatalf("reminder Check: %v", err)
	}
	if !alertSent {
		t.Fatal("reminder did not send alert")
	}

	week := isoWeek(thursdayAt10())
	pending, err := s.GetPendingRepoOp(ctx, week)
	if err != nil {
		t.Fatalf("GetPendingRepoOp: %v", err)
	}
	if pending == nil {
		t.Fatal("pending repo op not created")
	}

	// 2. 飞书回复 SKIP → 确认 skipped。
	replyStore := &PgStoreReplyAdapter{Store: s}
	handler := NewRepoReplyHandler(replyStore)
	handler.SetNowFn(thursdayAt10)
	reply, err := handler.HandleReply(ctx, ReplyEvent{
		EventID:      "evt-skip-flow",
		MessageID:    "msg-skip-flow",
		ChatID:       "oc_integration",
		SenderOpenID: "ou_owner",
		MessageType:  "text",
		Content:      `{"text":"SKIP 周末不在家"}`,
	})
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if reply == "" {
		t.Error("reply empty, want confirmation text")
	}

	// 3. 验证 repo op 已确认 skipped（pending 查询返回 nil）。
	confirmed, err := s.GetPendingRepoOp(ctx, week)
	if err != nil {
		t.Fatalf("GetPendingRepoOp after skip: %v", err)
	}
	if confirmed != nil {
		t.Error("pending repo op still exists after skip, want nil (status=skipped)")
	}

	// 4. 验证 cnfi_actions 未写入（SKIP 不写投资笔记）。
	actions, err := s.ListCNFIActions(ctx, 10)
	if err != nil {
		t.Fatalf("ListCNFIActions: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("actions count = %d, want 0 (SKIP 不写 cnfi_actions)", len(actions))
	}
}

// TestIntegration_ReminderStopAfterConfirm 确认后提醒服务不再触发。
func TestIntegration_ReminderStopAfterConfirm(t *testing.T) {
	s := integrationTestStore(t)
	ctx := context.Background()
	insertTestRepoRates(t, s, 3.5)
	setTestRepoConfig(t, s)

	// 1. 提醒触发 → 创建 pending。
	reminderStore := &PgStoreReminderAdapter{Store: s}
	alertCount := 0
	svc := NewRepoReminderService(reminderStore, func(level, msg string) {
		alertCount++
		_ = s.InsertAlert(ctx, nil, level, msg)
	})
	svc.SetNowFn(thursdayAt10)
	if err := svc.Check(ctx); err != nil {
		t.Fatalf("reminder Check 1: %v", err)
	}
	if alertCount != 1 {
		t.Fatalf("alert count after Thursday = %d, want 1", alertCount)
	}

	// 2. OK 确认。
	replyStore := &PgStoreReplyAdapter{Store: s}
	handler := NewRepoReplyHandler(replyStore)
	handler.SetNowFn(thursdayAt10)
	_, err := handler.HandleReply(ctx, ReplyEvent{
		ChatID:       "oc_integration",
		SenderOpenID: "ou_owner",
		MessageType:  "text",
		Content:      `{"text":"OK GC001 50000 2.85"}`,
	})
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}

	// 3. 周五再提醒 → 无新操作（已确认）。
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 9, 11, 10, 0, 0, 0, loc)
	svc.SetNowFn(func() time.Time { return friday })
	if err := svc.Check(ctx); err != nil {
		t.Fatalf("reminder Check Friday: %v", err)
	}
	if alertCount != 1 {
		t.Errorf("alert count after Friday = %d, want 1 (已确认不再提醒)", alertCount)
	}
}

// 确保未设置 DSN 时有明确提示（D-207）。
func TestIntegration_DSNRequired(t *testing.T) {
	if os.Getenv("ARB_TEST_PG_DSN") == "" {
		if os.Getenv("ARB_TEST_ALLOW_SKIP_DSN") == "1" {
			t.Skip("ARB_TEST_PG_DSN 未设置（ARB_TEST_ALLOW_SKIP_DSN=1 逃生门）")
		}
		t.Fatal("D-207：ARB_TEST_PG_DSN 未设置——集成测试需要真实 DB")
	}
}

// TestReply_EventIDMessageIDPersisted 确认 ev.EventID/ev.MessageID 写入 DB（非空字符串）。
func TestReply_EventIDMessageIDPersisted(t *testing.T) {
	s := integrationTestStore(t)
	ctx := context.Background()
	// 1. 创建 pending repo op（直接调 Store.CreateRepoOp）。
	triggerDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	id, err := s.CreateRepoOp(ctx, "2026-W37", "204001", "GC001", 3.5, 50.0, 3, "{}", triggerDate)
	if err != nil {
		t.Fatalf("CreateRepoOp: %v", err)
	}
	_ = id
	// 2. 飞书回复 OK，带 EventID/MessageID。
	replyStore := &PgStoreReplyAdapter{Store: s}
	handler := NewRepoReplyHandler(replyStore)
	handler.SetNowFn(thursdayAt10)
	reply, err := handler.HandleReply(ctx, ReplyEvent{
		EventID:      "evt-persist-1",
		MessageID:    "msg-persist-1",
		ChatID:       "oc_integration",
		SenderOpenID: "ou_owner",
		MessageType:  "text",
		Content:      `{"text":"OK GC001 50000 2.85"}`,
	})
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if reply == "" {
		t.Fatal("reply empty")
	}
	// 3. 查 DB 断言 feishu_event_id 和 feishu_message_id 非空且等于传入值。
	var evID, msgID string
	err = s.Pool().QueryRow(ctx,
		`SELECT feishu_event_id, feishu_message_id FROM cnfi_repo_ops WHERE opportunity_week='2026-W37'`).
		Scan(&evID, &msgID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if evID != "evt-persist-1" {
		t.Errorf("feishu_event_id = %q, want evt-persist-1", evID)
	}
	if msgID != "msg-persist-1" {
		t.Errorf("feishu_message_id = %q, want msg-persist-1", msgID)
	}
}
