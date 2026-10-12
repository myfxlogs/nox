package pgstore

import (
	"context"
	"testing"
)

// TestInsertAlert_HookFired D-114：SetAlertHook 注入回调 → InsertAlert → 回调被调用，
// 参数=完整 Alert（含 id/ts/level/message/acked，rule_id 透传）。
// rule_id 用 nil（外键约束 rules 表，测试库无种子规则行；nil 允许 NULL）。
func TestInsertAlert_HookFired(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE alerts: %v", err)
	}

	var got Alert
	s.SetAlertHook(func(a Alert) { got = a })

	if err := s.InsertAlert(ctx, nil, "warn", "hook-fired"); err != nil {
		t.Fatalf("InsertAlert: %v", err)
	}

	// 回调被调用且字段完整
	if got.Message != "hook-fired" {
		t.Errorf("hook alert Message = %q, want \"hook-fired\"", got.Message)
	}
	if got.Level != "warn" {
		t.Errorf("hook alert Level = %q, want \"warn\"", got.Level)
	}
	if got.ID == 0 {
		t.Error("hook alert ID = 0, want non-zero (RETURNING id)")
	}
	if got.Ts.IsZero() {
		t.Error("hook alert Ts is zero, want non-zero (RETURNING ts)")
	}
	if got.Acked {
		t.Error("hook alert Acked = true, want false (新插入未确认)")
	}
	if got.RuleID != nil {
		t.Errorf("hook alert RuleID = %v, want nil", got.RuleID)
	}
}

// TestInsertAlert_HookNil D-114：未注入 hook → InsertAlert 正常写入，不 panic。
func TestInsertAlert_HookNil(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE alerts: %v", err)
	}

	// 不调 SetAlertHook（alertHook 保持 nil）→ 不应 panic
	if err := s.InsertAlert(ctx, nil, "info", "no-hook"); err != nil {
		t.Fatalf("InsertAlert without hook: %v", err)
	}

	// 验证已写入
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM alerts WHERE message='no-hook'").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 row, got %d", n)
	}
}

// TestInsertAlert_Paused D-114：alertPaused=true → 不写入不回调。
func TestInsertAlert_Paused(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE alerts: %v", err)
	}

	hookCalled := false
	s.SetAlertHook(func(a Alert) { hookCalled = true })
	s.SetAlertPaused(true)

	if err := s.InsertAlert(ctx, nil, "warn", "paused"); err != nil {
		t.Fatalf("InsertAlert paused: %v", err)
	}

	if hookCalled {
		t.Error("hook called during alertPaused, want no callback")
	}

	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM alerts").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 rows (paused), got %d", n)
	}
}
