package autotrade

import (
	"arb/internal/risk/gate"
	"testing"
	"time"
)

// TestNextState_RunningStay RUNNING 无触发保持。
func TestNextState_RunningStay(t *testing.T) {
	next, _ := gate.NextState(gate.StateRunning, 0, 100, false, time.Now(), nil)
	if next != gate.StateRunning {
		t.Fatalf("want RUNNING, got %s", next)
	}
}

// TestNextState_DailyLossTrigger 日亏触发 DAILY_LOSS_HALT。
func TestNextState_DailyLossTrigger(t *testing.T) {
	next, reason := gate.NextState(gate.StateRunning, -150, 100, false, time.Now(), nil)
	if next != gate.StateDailyLossHalt {
		t.Fatalf("want DAILY_LOSS_HALT, got %s", next)
	}
	if reason == "" {
		t.Fatal("want non-empty reason")
	}
}

// TestNextState_AllDisabled 全策略禁用触发 STRATEGY_EXHAUSTED。
func TestNextState_AllDisabled(t *testing.T) {
	next, _ := gate.NextState(gate.StateRunning, 0, 100, true, time.Now(), nil)
	if next != gate.StateStrategyExhausted {
		t.Fatalf("want STRATEGY_EXHAUSTED, got %s", next)
	}
}

// TestNextState_CrossDayRecover 跨 UTC 日自动恢复 RUNNING。
func TestNextState_CrossDayRecover(t *testing.T) {
	haltedOn := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now := haltedOn.Add(25 * time.Hour)
	next, _ := gate.NextState(gate.StateDailyLossHalt, -150, 100, false, now, &haltedOn)
	if next != gate.StateRunning {
		t.Fatalf("want RUNNING (cross-day recover), got %s", next)
	}
}

// TestNextState_SameDayHalt 同日保持 DAILY_LOSS_HALT。
func TestNextState_SameDayHalt(t *testing.T) {
	haltedOn := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	now := haltedOn.Add(6 * time.Hour)
	next, _ := gate.NextState(gate.StateDailyLossHalt, -150, 100, false, now, &haltedOn)
	if next != gate.StateDailyLossHalt {
		t.Fatalf("want DAILY_LOSS_HALT (same day), got %s", next)
	}
}

// TestNextState_StrategyExhaustedManual STRATEGY_EXHAUSTED 仅人工恢复。
func TestNextState_StrategyExhaustedManual(t *testing.T) {
	next, _ := gate.NextState(gate.StateStrategyExhausted, 0, 100, false, time.Now(), nil)
	if next != gate.StateStrategyExhausted {
		t.Fatalf("want STRATEGY_EXHAUSTED (manual only), got %s", next)
	}
}

// TestNextState_CloseOnlyManual CLOSE_ONLY 仅人工恢复。
func TestNextState_CloseOnlyManual(t *testing.T) {
	next, _ := gate.NextState(gate.StateCloseOnly, 0, 100, false, time.Now(), nil)
	if next != gate.StateCloseOnly {
		t.Fatalf("want CLOSE_ONLY (manual only), got %s", next)
	}
}
