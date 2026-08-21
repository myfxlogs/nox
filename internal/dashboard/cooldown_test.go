package dashboard

import (
	"errors"
	"testing"
	"time"
)

// TestCooldown_EnterCooling 连续超时 ≥2 → 进入冷却，Skip 返回 true。
func TestCooldown_EnterCooling(t *testing.T) {
	cd := NewCooldown(2, 50*time.Millisecond)
	// 2 次超时 → 进入冷却
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", errors.New("timeout"))
	if !cd.Cooling("vA") {
		t.Error("after 2 consecutive timeouts want cooling=true")
	}
	if !cd.Skip("vA") {
		t.Error("cooling key want Skip=true")
	}
}

// TestCooldown_SuccessClears 成功 → 连续计数清零。
func TestCooldown_SuccessClears(t *testing.T) {
	cd := NewCooldown(2, 50*time.Millisecond)
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", nil) // 成功清零
	if cd.Consecutive("vA") != 0 {
		t.Errorf("after success want consecutive=0, got %d", cd.Consecutive("vA"))
	}
	cd.Record("vA", errors.New("timeout")) // 1 次，未达阈值
	if cd.Cooling("vA") {
		t.Error("1 timeout (after clear) want cooling=false")
	}
}

// TestCooldown_CooldownExpires 冷却到期 → Skip 返回 false（退出冷却）。
func TestCooldown_CooldownExpires(t *testing.T) {
	cd := NewCooldown(2, 20*time.Millisecond)
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", errors.New("timeout"))
	if !cd.Cooling("vA") {
		t.Fatal("want cooling=true before expire")
	}
	time.Sleep(30 * time.Millisecond) // 等冷却到期
	if cd.Skip("vA") {
		t.Error("after cooldown expire want Skip=false")
	}
	if cd.Cooling("vA") {
		t.Error("after cooldown expire want Cooling=false")
	}
}

// TestCooldown_BelowThreshold 连续超时 < 阈值 → 不进入冷却。
func TestCooldown_BelowThreshold(t *testing.T) {
	cd := NewCooldown(3, 50*time.Millisecond) // 阈值=3
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", errors.New("timeout")) // 2 < 3
	if cd.Cooling("vA") {
		t.Error("2 timeouts < threshold 3 want cooling=false")
	}
	if cd.Skip("vA") {
		t.Error("below threshold want Skip=false")
	}
}

// TestCooldown_PerKeyIndependent 各 key 独立冷却。
func TestCooldown_PerKeyIndependent(t *testing.T) {
	cd := NewCooldown(2, 50*time.Millisecond)
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", errors.New("timeout")) // vA 进入冷却
	cd.Record("vB", errors.New("timeout")) // vB 仅 1 次
	if !cd.Cooling("vA") {
		t.Error("vA want cooling=true")
	}
	if cd.Cooling("vB") {
		t.Error("vB want cooling=false (only 1 timeout)")
	}
}

// TestCooldown_Defaults 零值参数 → 默认值（threshold=2, dur=10s）。
func TestCooldown_Defaults(t *testing.T) {
	cd := NewCooldown(0, 0)
	cd.Record("vA", errors.New("timeout"))
	cd.Record("vA", errors.New("timeout"))
	if !cd.Cooling("vA") {
		t.Error("default threshold=2, 2 timeouts want cooling=true")
	}
}
