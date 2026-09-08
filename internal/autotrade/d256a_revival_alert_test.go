// D-256-A S5 单测：复活扳机告警（雷达有人值守）。
// 验证 settled + over threshold → alert；1h throttle；spec-only → no alert；
// disabled 策略仍被扫描（maybeRevivalAlert 在 trySwapArb 之前调用）。
package autotrade

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// TestD256A_S5_SettledOverThresholdAlerts settled + netSwap >= 阈值 → critical 告警。
func TestD256A_S5_SettledOverThresholdAlerts(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	var mu sync.Mutex
	var alerts []string
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 2.5, swapSrcSettled)

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("settled+over: want 1 alert, got %d", len(alerts))
	}
	if !strings.HasPrefix(alerts[0], "critical:") {
		t.Errorf("level: want critical, got %s", alerts[0])
	}
	if !strings.Contains(alerts[0], "revival") {
		t.Errorf("msg: want 'revival', got %s", alerts[0])
	}
}

// TestD256A_S5_SpecSourceNoAlert spec 源不告警（仅 settled 实测值触发）。
func TestD256A_S5_SpecSourceNoAlert(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	called := false
	e.AlertSink = func(level, msg string) { called = true }
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 3.0, swapSrcSpec)

	if called {
		t.Error("spec source: should not alert")
	}
}

// TestD256A_S5_BelowThresholdNoAlert settled 但 netSwap < 阈值 → 不告警。
func TestD256A_S5_BelowThresholdNoAlert(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	called := false
	e.AlertSink = func(level, msg string) { called = true }
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 1.5, swapSrcSettled)

	if called {
		t.Error("below threshold: should not alert")
	}
}

// TestD256A_S5_ThrottleOneHour 同 combo 1h 内不重复告警。
func TestD256A_S5_ThrottleOneHour(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	var mu sync.Mutex
	var n int
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		n++
		mu.Unlock()
	}
	ck := "CARRY:AUDJPY:vA:mt5:vB:mt5"
	e.maybeRevivalAlert(ck, "CARRY", 2.5, swapSrcSettled)
	e.maybeRevivalAlert(ck, "CARRY", 2.5, swapSrcSettled) // 1h 内重复 → 节流

	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Errorf("throttle: want 1 alert (1h dedup), got %d", n)
	}
}

// TestD256A_S5_ThrottleDifferentCombos 不同 combo 各发一条。
func TestD256A_S5_ThrottleDifferentCombos(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	var mu sync.Mutex
	var n int
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		n++
		mu.Unlock()
	}
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 2.5, swapSrcSettled)
	e.maybeRevivalAlert("SWAPARB:USDJPY:vA:mt5:vB:mt5", "SWAPARB", 2.5, swapSrcSettled)

	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Errorf("different combos: want 2 alerts, got %d", n)
	}
}

// TestD256A_S5_DisabledStrategyStillScanned disabled 策略仍被扫描评估（maybeRevivalAlert 不检查 disabled）。
// 设计要求：复活扳机在 trySwapArb 之前调用，disabled 只在 trySwapArb 内拦提交。
func TestD256A_S5_DisabledStrategyStillScanned(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
	}
	called := false
	e.AlertSink = func(level, msg string) { called = true }
	// 不检查 disabled，直接调 maybeRevivalAlert（模拟 scanSwapArb 在 trySwapArb 之前调用）
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 2.5, swapSrcSettled)

	if !called {
		t.Error("disabled strategy: should still be scanned for revival alert")
	}
}

// TestD256A_S5_NilAlertSinkNoOp AlertSink nil → no-op 不 panic。
func TestD256A_S5_NilAlertSinkNoOp(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 2.2},
		revivalAlertSent: make(map[string]time.Time),
		// AlertSink nil
	}
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 2.5, swapSrcSettled) // 不应 panic
}

// TestD256A_S5_RevivalBpsZeroDisabled SwapArbRevivalBps=0 → no-op。
func TestD256A_S5_RevivalBpsZeroDisabled(t *testing.T) {
	e := &Engine{
		cfg:              Config{SwapArbRevivalBps: 0},
		revivalAlertSent: make(map[string]time.Time),
	}
	called := false
	e.AlertSink = func(level, msg string) { called = true }
	e.maybeRevivalAlert("CARRY:AUDJPY:vA:mt5:vB:mt5", "CARRY", 2.5, swapSrcSettled)

	if called {
		t.Error("SwapArbRevivalBps=0: should not alert (disabled)")
	}
}
