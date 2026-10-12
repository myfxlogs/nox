package pgstore

import (
	"context"
	"testing"
)

// execution_controls CRUD 测试（DSN-gated 真库）。

func testExecutionControlsStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE execution_controls, execution_control_events`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// TestExecutionControls_GetEmpty：无控制行 → 返回 nil（调用方按 READ_ONLY fail-closed）。
func TestExecutionControls_GetEmpty(t *testing.T) {
	s := testExecutionControlsStore(t)
	ctx := context.Background()

	c, err := s.GetExecutionControl(ctx, "global", "")
	if err != nil {
		t.Fatalf("GetExecutionControl: %v", err)
	}
	if c != nil {
		t.Errorf("返回非 nil %+v，无行应返回 nil", c)
	}
}

// TestExecutionControlsEventAppend：UpsertExecutionControl → 事件追加 + event_key 幂等。
func TestExecutionControlsEventAppend(t *testing.T) {
	s := testExecutionControlsStore(t)
	ctx := context.Background()

	// 首次 upsert + 事件
	ctrl := &ExecutionControl{
		ScopeType: "global",
		ScopeKey:  "",
		Mode:      "PAPER",
		Enabled:   true,
		Reason:    "test",
	}
	event := &ExecutionControlEvent{
		ScopeType: "global",
		ScopeKey:  "",
		FromMode:  "READ_ONLY",
		ToMode:    "PAPER",
		Actor:     "test",
		Reason:    "switch to paper",
		EventKey:  "evt-001",
	}
	if err := s.UpsertExecutionControl(ctx, ctrl, event); err != nil {
		t.Fatalf("UpsertExecutionControl: %v", err)
	}

	// 验证控制行
	c, err := s.GetExecutionControl(ctx, "global", "")
	if err != nil {
		t.Fatalf("GetExecutionControl: %v", err)
	}
	if c == nil || c.Mode != "PAPER" {
		t.Errorf("控制行 mode=%v, want PAPER", c)
	}

	// 验证事件追加
	events, err := s.ListExecutionControlEvents(ctx, "global", "")
	if err != nil {
		t.Fatalf("ListExecutionControlEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("事件数=%d, want 1", len(events))
	}
	if events[0].EventKey != "evt-001" {
		t.Errorf("EventKey=%s, want evt-001", events[0].EventKey)
	}

	// 幂等：同 event_key 二次追加 → 不重复
	if err := s.AppendExecutionControlEvent(ctx, event); err != nil {
		t.Fatalf("AppendExecutionControlEvent 2: %v", err)
	}
	events2, _ := s.ListExecutionControlEvents(ctx, "global", "")
	if len(events2) != 1 {
		t.Errorf("幂等后事件数=%d, want 1", len(events2))
	}
}

// TestExecutionControls_UpdateMode：upsert 更新 mode + 追加新事件。
func TestExecutionControls_UpdateMode(t *testing.T) {
	s := testExecutionControlsStore(t)
	ctx := context.Background()

	// 首次 PAPER
	ctrl1 := &ExecutionControl{ScopeType: "global", ScopeKey: "", Mode: "PAPER", Enabled: true}
	evt1 := &ExecutionControlEvent{ScopeType: "global", ScopeKey: "", ToMode: "PAPER", EventKey: "evt-1"}
	if err := s.UpsertExecutionControl(ctx, ctrl1, evt1); err != nil {
		t.Fatalf("Upsert 1: %v", err)
	}

	// 更新 TEST
	ctrl2 := &ExecutionControl{ScopeType: "global", ScopeKey: "", Mode: "TEST", Enabled: true}
	evt2 := &ExecutionControlEvent{ScopeType: "global", ScopeKey: "", ToMode: "TEST", EventKey: "evt-2"}
	if err := s.UpsertExecutionControl(ctx, ctrl2, evt2); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}

	c, _ := s.GetExecutionControl(ctx, "global", "")
	if c == nil || c.Mode != "TEST" {
		t.Errorf("更新后 mode=%v, want TEST", c)
	}

	events, _ := s.ListExecutionControlEvents(ctx, "global", "")
	if len(events) != 2 {
		t.Errorf("事件数=%d, want 2", len(events))
	}
}

// TestExecutionControls_KillSwitchEnabledFalse：enabled=false → KillSwitch 激活。
func TestExecutionControls_KillSwitchEnabledFalse(t *testing.T) {
	s := testExecutionControlsStore(t)
	ctx := context.Background()

	ctrl := &ExecutionControl{
		ScopeType: "global",
		ScopeKey:  "",
		Mode:      "READ_ONLY",
		Enabled:   false,
		Reason:    "kill switch",
	}
	if err := s.UpsertExecutionControl(ctx, ctrl, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	c, _ := s.GetExecutionControl(ctx, "global", "")
	if c == nil {
		t.Fatal("控制行不存在")
	}
	if c.Enabled {
		t.Errorf("Enabled=true, want false (kill switch active)")
	}
}

// TestExecutionControls_ListControls：列出所有控制行。
func TestExecutionControls_ListControls(t *testing.T) {
	s := testExecutionControlsStore(t)
	ctx := context.Background()

	// 插入 global
	ctrl := &ExecutionControl{ScopeType: "global", ScopeKey: "", Mode: "PAPER", Enabled: true}
	if err := s.UpsertExecutionControl(ctx, ctrl, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	list, err := s.ListExecutionControls(ctx)
	if err != nil {
		t.Fatalf("ListExecutionControls: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("控制行数=%d, want 1", len(list))
	}
	if list[0].Mode != "PAPER" {
		t.Errorf("mode=%s, want PAPER", list[0].Mode)
	}
}
