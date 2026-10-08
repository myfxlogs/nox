package autotrade

import (
	"context"
	"testing"
)

// D-195-C3 S1：重启恢复 + 数据库恢复测试（§10.1 场景 13/14）。
// 「进程重启」= 重新构造 ControlPlane/LiveGateChecker 实例（同 DB，模拟新进程读持久化状态）。
// 「数据库恢复」= 备份恢复后 execution_controls 仍为 READ_ONLY（migration 0062 初始插入 global/READ_ONLY）。

// TestRestart_LiveGateClosed（§10.1.13）：进程重启后 LiveGate 为关闭（无 live_gates 行 → Passed=false）。
func TestRestart_LiveGateClosed(t *testing.T) {
	s := setupControlPlaneStore(t)
	ctx := context.Background()

	// 清 live_gates（确保无行）
	if _, err := s.Pool().Exec(ctx, `TRUNCATE live_gates`); err != nil {
		t.Fatalf("truncate live_gates: %v", err)
	}

	// 「重启」= 重新构造 LiveGateChecker（模拟新进程）
	lg := NewLiveGateChecker(s, nil)
	result := lg.Check(ctx, "", "", "")
	if result.Passed {
		t.Fatal("重启后 LiveGate 应关闭（Passed=false，无 live_gates 行），但 Passed=true")
	}
	if result.HasGateRecord {
		t.Error("无 live_gates 行时 HasGateRecord 应为 false")
	}
	if len(result.Reasons) == 0 {
		t.Error("LiveGate 关闭应附带失败原因")
	}
}

// TestRestart_ExecutionModeReadOnly：重启后 ExecutionMode 为 READ_ONLY。
// migration 0062 初始插入 global/READ_ONLY 行；ControlPlane.GetMode 无行时 fail-closed 返回 READ_ONLY。
func TestRestart_ExecutionModeReadOnly(t *testing.T) {
	s := setupControlPlaneStore(t)
	ctx := context.Background()

	// 「重启」= 重新构造 ControlPlane（模拟新进程读持久化状态）
	cp := NewControlPlane(s)
	mode, err := cp.GetMode(ctx)
	if err != nil {
		t.Fatalf("GetMode: %v", err)
	}
	if mode != ModeReadOnly {
		t.Errorf("重启后 mode=%s, want READ_ONLY", mode)
	}
}

// TestDBRecovery_StillReadOnly（§10.1.14）：数据库恢复后仍为 READ_ONLY。
// 模拟：备份时 mode=READ_ONLY → 恢复后 execution_controls.mode = READ_ONLY。
// 前提：migration 0062 初始插入 global/READ_ONLY 行，ControlPlane.GetMode 无行时 fail-closed 返回 READ_ONLY。
func TestDBRecovery_StillReadOnly(t *testing.T) {
	s := setupControlPlaneStore(t)
	ctx := context.Background()

	// 确认备份时（当前状态）mode=READ_ONLY
	cp := NewControlPlane(s)
	mode, err := cp.GetMode(ctx)
	if err != nil {
		t.Fatalf("GetMode before recovery: %v", err)
	}
	if mode != ModeReadOnly {
		t.Fatalf("备份时 mode=%s, want READ_ONLY（前提）", mode)
	}

	// 模拟「数据库恢复」：直接查询 execution_controls 表（恢复后表内容不变）
	var dbMode string
	err = s.Pool().QueryRow(ctx,
		`SELECT mode FROM execution_controls WHERE scope_type='global' AND scope_key=''`,
	).Scan(&dbMode)
	if err != nil {
		// 无行 = fail-closed READ_ONLY（ControlPlane.GetMode 返回 READ_ONLY）
		cp2 := NewControlPlane(s)
		recoveredMode, _ := cp2.GetMode(ctx)
		if recoveredMode != ModeReadOnly {
			t.Errorf("恢复后无行 → GetMode=%s, want READ_ONLY (fail-closed)", recoveredMode)
		}
		return
	}
	if dbMode != ModeReadOnly {
		t.Errorf("恢复后 execution_controls.mode=%s, want READ_ONLY", dbMode)
	}

	// 「恢复后」重新构造 ControlPlane 验证
	cp2 := NewControlPlane(s)
	recoveredMode, err := cp2.GetMode(ctx)
	if err != nil {
		t.Fatalf("GetMode after recovery: %v", err)
	}
	if recoveredMode != ModeReadOnly {
		t.Errorf("恢复后 GetMode=%s, want READ_ONLY", recoveredMode)
	}
}

// TestRestart_KillSwitchSurvivesRestart：KillSwitch 状态跨重启持久化（enabled=false 持久化）。
func TestRestart_KillSwitchSurvivesRestart(t *testing.T) {
	s := setupControlPlaneStore(t)
	ctx := context.Background()

	cp := NewControlPlane(s)
	if err := cp.TriggerKillSwitch(ctx, "test", "restart recovery test"); err != nil {
		t.Fatalf("TriggerKillSwitch: %v", err)
	}

	// 「重启」= 重新构造 ControlPlane
	cp2 := NewControlPlane(s)
	active, err := cp2.IsKillSwitchActive(ctx)
	if err != nil {
		t.Fatalf("IsKillSwitchActive after restart: %v", err)
	}
	if !active {
		t.Error("KillSwitch 状态应跨重启持久化（enabled=false），但 IsKillSwitchActive=false")
	}

	mode, _, _, _ := cp2.CheckGate(ctx)
	if mode != ModeReadOnly {
		t.Errorf("KillSwitch 激活后 mode=%s, want READ_ONLY", mode)
	}
}

// TestRestart_PaperModeSurvivesRestart：PAPER 模式跨重启持久化（验证非 READ_ONLY 状态也能恢复）。
func TestRestart_PaperModeSurvivesRestart(t *testing.T) {
	s := setupControlPlaneStore(t)
	ctx := context.Background()

	cp := NewControlPlane(s)
	if err := cp.SetMode(ctx, ModePaper, "test", "restart recovery test"); err != nil {
		t.Fatalf("SetMode PAPER: %v", err)
	}

	// 「重启」= 重新构造 ControlPlane
	cp2 := NewControlPlane(s)
	mode, err := cp2.GetMode(ctx)
	if err != nil {
		t.Fatalf("GetMode after restart: %v", err)
	}
	if mode != ModePaper {
		t.Errorf("PAPER 模式应跨重启持久化，got mode=%s, want PAPER", mode)
	}

	// 清理：恢复 READ_ONLY
	if err := cp2.SetMode(ctx, ModeReadOnly, "test", "cleanup"); err != nil {
		t.Fatalf("cleanup SetMode: %v", err)
	}
}
