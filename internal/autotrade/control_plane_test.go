package autotrade

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// D-195-A：ControlPlane 执行控制面测试（DSN-gated 真库）。
// 覆盖 D-195 §10.1 必测场景 1/3/13/14 + LIVE 拒绝 + fail-closed + autotrade_state 不能提升。

func setupControlPlaneStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	// 清 execution_controls + execution_control_events
	if _, err := s.Pool().Exec(ctx, `TRUNCATE execution_controls, execution_control_events`); err != nil {
		t.Fatalf("truncate execution_controls: %v", err)
	}
	return s
}

// TestControlPlane_DefaultReadOnly（§10.1.14）：初始化后 global mode=READ_ONLY → CheckGate allowed=false。
func TestControlPlane_DefaultReadOnly(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	mode, allowed, reason, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate: %v", err)
	}
	if mode != ModeReadOnly {
		t.Errorf("mode=%s, want READ_ONLY (无控制行 fail-closed)", mode)
	}
	if allowed {
		t.Errorf("allowed=true, want false (READ_ONLY)")
	}
	if reason == "" {
		t.Error("reason 为空，应有说明")
	}
}

// TestControlPlane_SetModePaper（§10.1.3）：SetMode(PAPER) → CheckGate allowed=true。
func TestControlPlane_SetModePaper(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	if err := cp.SetMode(ctx, ModePaper, "test", "D-195-A test"); err != nil {
		t.Fatalf("SetMode(PAPER): %v", err)
	}
	mode, allowed, _, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate: %v", err)
	}
	if mode != ModePaper {
		t.Errorf("mode=%s, want PAPER", mode)
	}
	if !allowed {
		t.Errorf("allowed=false, want true (PAPER)")
	}
}

// TestControlPlane_KillSwitchTrigger（§10.1.1）：TriggerKillSwitch → CheckGate allowed=false。
func TestControlPlane_KillSwitchTrigger(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	// 先设 PAPER（allowed=true）
	if err := cp.SetMode(ctx, ModePaper, "test", "setup"); err != nil {
		t.Fatalf("SetMode(PAPER): %v", err)
	}
	// 触发 KillSwitch
	if err := cp.TriggerKillSwitch(ctx, "test", "manual kill"); err != nil {
		t.Fatalf("TriggerKillSwitch: %v", err)
	}
	mode, allowed, reason, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate: %v", err)
	}
	if allowed {
		t.Errorf("allowed=true, want false (kill switch active)")
	}
	if mode != ModeReadOnly {
		t.Errorf("mode=%s, want READ_ONLY (kill switch 设 READ_ONLY)", mode)
	}
	if reason != "kill switch active" {
		t.Errorf("reason=%s, want 'kill switch active'", reason)
	}
}

// TestControlPlane_KillSwitchResolve（§10.1.13）：ResolveKillSwitch → CheckGate allowed=true（需显式人工）。
func TestControlPlane_KillSwitchResolve(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	// 触发 KillSwitch
	if err := cp.TriggerKillSwitch(ctx, "test", "manual kill"); err != nil {
		t.Fatalf("TriggerKillSwitch: %v", err)
	}
	// 解除 KillSwitch（显式人工）
	if err := cp.ResolveKillSwitch(ctx, "operator", "reviewed", "evidence-001"); err != nil {
		t.Fatalf("ResolveKillSwitch: %v", err)
	}
	// 解除后 mode 仍 READ_ONLY → allowed=false（需显式 SetMode 提升）
	mode, allowed, _, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate: %v", err)
	}
	if mode != ModeReadOnly {
		t.Errorf("mode=%s, want READ_ONLY (解除后仍 READ_ONLY)", mode)
	}
	if allowed {
		t.Errorf("allowed=true, want false (解除后仍 READ_ONLY，需显式 SetMode 提升)")
	}

	// 显式 SetMode(PAPER) 后 → allowed=true
	if err := cp.SetMode(ctx, ModePaper, "operator", "post-kill-switch resume"); err != nil {
		t.Fatalf("SetMode(PAPER): %v", err)
	}
	_, allowed2, _, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate 2: %v", err)
	}
	if !allowed2 {
		t.Errorf("allowed=false after SetMode(PAPER), want true")
	}
}

// TestControlPlane_LiveModeRejected：SetMode(LIVE) → 返回错误（步骤 5 约束）。
func TestControlPlane_LiveModeRejected(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	err := cp.SetMode(ctx, "LIVE", "test", "attempt live")
	if err == nil {
		t.Error("SetMode(LIVE) 返回 nil，应返回错误（步骤 5 约束）")
	}
}

// TestControlPlane_QueryFailureFailClosed：store 查询失败 → CheckGate fail-closed（allowed=false）。
// 用 closed store 模拟查询失败。
func TestControlPlane_QueryFailureFailClosed(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	// 关闭 store 模拟查询失败
	s.Close()

	_, allowed, _, err := cp.CheckGate(ctx)
	if err == nil {
		t.Log("CheckGate 返回 nil err（可能 pool 缓存），检查 allowed")
	}
	if allowed {
		t.Errorf("allowed=true, want false (查询失败 fail-closed)")
	}
}

// TestControlPlane_AutotradeStateCannotElevate：execution_controls=READ_ONLY + autotrade_state=RUNNING
// → CheckGate 仍 allowed=false（autotrade_state 不能提升执行权限）。
func TestControlPlane_AutotradeStateCannotElevate(t *testing.T) {
	s := setupControlPlaneStore(t)
	cp := NewControlPlane(s)
	ctx := context.Background()

	// execution_controls 默认 READ_ONLY（无控制行）
	// autotrade_state=RUNNING
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state`); err != nil {
		t.Fatalf("truncate autotrade_state: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state) VALUES ('RUNNING')`); err != nil {
		t.Fatalf("insert autotrade_state RUNNING: %v", err)
	}

	// CheckGate 仍 allowed=false（autotrade_state 不能提升）
	_, allowed, _, err := cp.CheckGate(ctx)
	if err != nil {
		t.Fatalf("CheckGate: %v", err)
	}
	if allowed {
		t.Errorf("allowed=true, want false (autotrade_state=RUNNING 不能提升 READ_ONLY)")
	}
}
