package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// ControlPlane 执行控制面测试（DSN-gated 真库）。
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

// ---- D-620 T1：LIVE 真金单闸（GrantLive/撤权/续权/过期/readiness）----

func setupLiveCP(t *testing.T, ready bool) (*pgstore.Store, *ControlPlane) {
	t.Helper()
	s := setupControlPlaneStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE live_gates`); err != nil {
		t.Fatalf("truncate live_gates: %v", err)
	}
	cp := NewControlPlane(s)
	cp.SetLiveGateChecker(NewLiveGateChecker(s, func(context.Context) bool { return ready }, "live"))
	return s, cp
}

// TestControlPlane_GrantLive D-620 T1：授权→mode=LIVE+active 行+事件；
// 降级/kill 撤权；续权不撞键；过期/readiness=false 拒。
func TestControlPlane_GrantLive(t *testing.T) {
	s, cp := setupLiveCP(t, true)
	ctx := context.Background()
	exp := time.Now().Add(30 * 24 * time.Hour)

	// SetMode("LIVE") 直调=错（grant-only）。
	if err := cp.SetMode(ctx, ModeLive, "t", "t"); err == nil {
		t.Fatal("SetMode(LIVE) want error")
	}
	// expires_at 过去→错且零行。
	if err := cp.GrantLive(ctx, "op", "r", "D-620", time.Now().Add(-time.Second)); err == nil {
		t.Fatal("GrantLive 过去 expires: want error")
	}
	if g, _ := cp.ActiveLiveGate(ctx); g != nil {
		t.Fatal("过期 Grant 后 live_gates 应为零行")
	}
	// Grant 成功→mode=LIVE+active 行+事件 to_mode=LIVE。
	if err := cp.GrantLive(ctx, "op1", "首授", "D-620", exp); err != nil {
		t.Fatalf("GrantLive: %v", err)
	}
	mode, allowed, _, err := cp.CheckGate(ctx)
	if err != nil || mode != ModeLive || !allowed {
		t.Fatalf("post-grant CheckGate: mode=%s allowed=%v err=%v", mode, allowed, err)
	}
	g, _ := cp.ActiveLiveGate(ctx)
	if g == nil || g.Operator != "op1" {
		t.Fatalf("active live_gates 行缺或 operator 错: %+v", g)
	}
	var evCnt int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM execution_control_events WHERE to_mode='LIVE'`).Scan(&evCnt); err != nil || evCnt == 0 {
		t.Fatalf("to_mode=LIVE 事件行缺: %d %v", evCnt, err)
	}
	// 降级即撤权：SetMode(PAPER)→live_gates revoked 非空+mode=PAPER。
	if err := cp.SetMode(ctx, ModePaper, "op1", "降级"); err != nil {
		t.Fatalf("SetMode(PAPER): %v", err)
	}
	if g, _ := cp.ActiveLiveGate(ctx); g != nil {
		t.Fatal("降级后仍有 active live_gates 行（撤权未生效）")
	}
	var rvCnt int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM live_gates WHERE revoked_at IS NOT NULL`).Scan(&rvCnt); err != nil || rvCnt != 1 {
		t.Fatalf("revoked 行应为 1: %d %v", rvCnt, err)
	}
	// kill 后再 Grant 成功（部分唯一索引不撞键：active 1+revoked ≥1）。
	if err := cp.GrantLive(ctx, "op2", "续权", "D-620-续", exp); err != nil {
		t.Fatalf("续权 GrantLive: %v", err)
	}
	var actCnt int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM live_gates WHERE revoked_at IS NULL`).Scan(&actCnt); err != nil || actCnt != 1 {
		t.Fatalf("active 行应为 1: %d %v", actCnt, err)
	}
	// TriggerKillSwitch→revoked+enabled=false。
	if err := cp.TriggerKillSwitch(ctx, "op2", "测试 kill"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if g, _ := cp.ActiveLiveGate(ctx); g != nil {
		t.Fatal("kill 后仍有 active live_gates 行")
	}
	if ks, _ := cp.IsKillSwitchActive(ctx); !ks {
		t.Fatal("kill 后 enabled 应为 false")
	}
}

// TestControlPlane_LiveGateExpiredRejected D-620 T1 补：过期行 CheckGate 拒（反证锚②）。
func TestControlPlane_LiveGateExpiredRejected(t *testing.T) {
	s, cp := setupLiveCP(t, true)
	ctx := context.Background()
	if err := cp.GrantLive(ctx, "op", "r", "D-620", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE live_gates SET expires_at=now()-interval '1s' WHERE revoked_at IS NULL`); err != nil {
		t.Fatalf("expire gate: %v", err)
	}
	if _, allowed, reason, _ := cp.CheckGate(ctx); allowed || !strings.Contains(reason, "expired") {
		t.Fatalf("过期后应拒且 reason 含 expired: allowed=%v reason=%q", allowed, reason)
	}
}

// TestControlPlane_GrantLive_ReadinessDown D-620 T1 补：readiness=false→Grant 拒。
func TestControlPlane_GrantLive_ReadinessDown(t *testing.T) {
	_, cp := setupLiveCP(t, false)
	if err := cp.GrantLive(context.Background(), "op", "r", "D-620", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("readiness=false 时 GrantLive 应拒")
	}
}
