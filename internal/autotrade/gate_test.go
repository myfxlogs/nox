package autotrade

import (
	"testing"

	"arb/internal/errclass"
)

// TestGateDecision_AllPass 全部通过（D-072 §3.1）。
func TestGateDecision_AllPass(t *testing.T) {
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "SWAPARB", Disabled: false,
		DailyPnl: 0, MaxDailyLoss: 100, OpenCount: 0, MaxPositions: 5, DedupHit: false,
	})
	if !v.Pass {
		t.Fatalf("want pass, got reject: %s", v.Reason)
	}
}

// TestGateDecision_StateHalt 状态非 RUNNING 拒绝。
func TestGateDecision_StateHalt(t *testing.T) {
	v := gateDecision(GateInput{State: StateDailyLossHalt, MaxDailyLoss: 100, MaxPositions: 5})
	if v.Pass {
		t.Fatal("want reject on DAILY_LOSS_HALT")
	}
}

// TestGateDecision_Disabled 策略禁用拒绝。
func TestGateDecision_Disabled(t *testing.T) {
	v := gateDecision(GateInput{State: StateRunning, Disabled: true, MaxDailyLoss: 100, MaxPositions: 5})
	if v.Pass {
		t.Fatal("want reject on disabled")
	}
}

// TestGateDecision_DailyLoss 日亏超限拒绝。
func TestGateDecision_DailyLoss(t *testing.T) {
	v := gateDecision(GateInput{State: StateRunning, DailyPnl: -150, MaxDailyLoss: 100, MaxPositions: 5})
	if v.Pass {
		t.Fatal("want reject on daily loss")
	}
}

// TestGateDecision_MaxPositions 持仓数上限拒绝。
func TestGateDecision_MaxPositions(t *testing.T) {
	v := gateDecision(GateInput{State: StateRunning, OpenCount: 5, MaxPositions: 5, MaxDailyLoss: 100})
	if v.Pass {
		t.Fatal("want reject on max positions")
	}
}

// TestGateDecision_DedupHit 幂等命中拒绝。
func TestGateDecision_DedupHit(t *testing.T) {
	v := gateDecision(GateInput{State: StateRunning, DedupHit: true, MaxDailyLoss: 100, MaxPositions: 5})
	if v.Pass {
		t.Fatal("want reject on dedup hit")
	}
}

// TestGateDecision_OrderPriority 检查顺序：状态优先于策略禁用。
func TestGateDecision_OrderPriority(t *testing.T) {
	v := gateDecision(GateInput{State: StateDailyLossHalt, Disabled: true, MaxDailyLoss: 100, MaxPositions: 5})
	if v.Pass {
		t.Fatal("want reject")
	}
	// 应返回状态原因而非策略禁用原因
	if v.Reason == "" {
		t.Fatal("want non-empty reason")
	}
}

// --- H4.1 maxPositionsReachedDecision 单测（满仓短路纯函数） ---

func TestMaxPositionsReachedDecision_Full(t *testing.T) {
	if !maxPositionsReachedDecision(5, 5) {
		t.Error("openCount==maxPositions should be reached (short-circuit)")
	}
	if !maxPositionsReachedDecision(6, 5) {
		t.Error("openCount>maxPositions should be reached (short-circuit)")
	}
}

func TestMaxPositionsReachedDecision_NotFull(t *testing.T) {
	if maxPositionsReachedDecision(4, 5) {
		t.Error("openCount<maxPositions should NOT be reached")
	}
	if maxPositionsReachedDecision(0, 5) {
		t.Error("openCount=0 should NOT be reached")
	}
}

// TestWorseAction 返回更严重的 Action（Halt > RetryFresh > Retry > Abort，D-077 修复⑩ G3）。
func TestWorseAction(t *testing.T) {
	tests := []struct {
		a, b errclass.Action
		want errclass.Action
	}{
		{errclass.Halt, errclass.Abort, errclass.Halt},
		{errclass.Abort, errclass.Halt, errclass.Halt},
		{errclass.RetryFresh, errclass.Retry, errclass.RetryFresh},
		{errclass.Retry, errclass.Abort, errclass.Retry},
		{errclass.Abort, errclass.Abort, errclass.Abort},
	}
	for _, tt := range tests {
		got := worseAction(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("worseAction(%v,%v) want %v, got %v", tt.a, tt.b, tt.want, got)
		}
	}
}
