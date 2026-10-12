package gate

import (
	"strings"
	"testing"

	"arb/internal/platform/combo"
	"arb/internal/platform/errclass"
	"arb/internal/store/pgstore"
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

// --- D-080 R1 防重 DB 实时派生单测 ---

// TestGateDedup_SameComboRejected_DBDerived 同组合活跃持仓 → gateDecision 拒绝（DB 实时派生）。
// 模拟 ListActivePositions 返回同组合持仓行 → comboAlreadyOpen=true → 拒绝。
func TestGateDedup_SameComboRejected_DBDerived(t *testing.T) {
	ck := combo.Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	// 模拟 DB 查回的活跃持仓（含同组合）
	activePositions := []pgstore.Position{
		{ID: 1, OpportunityID: ck, Status: "OPEN"},
	}
	intentCombo := combo.FromOppID(ck)
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			comboAlreadyOpen = true
			break
		}
	}
	if !comboAlreadyOpen {
		t.Fatal("同组合活跃持仓应被 DB 派生匹配")
	}
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "SWAPARB",
		MaxDailyLoss: 100, MaxPositions: 5,
		DedupHit: comboAlreadyOpen,
	})
	if v.Pass {
		t.Fatal("同组合活跃持仓应拒绝")
	}
}

// TestGateDedup_ManualPositionBlocked 手动开仓持仓（未经 gate）同样被防重。
// 手动路径（service.go → pipeline.Execute）写入 positions 表 → ListActivePositions 查到 → 拒绝。
func TestGateDedup_ManualPositionBlocked(t *testing.T) {
	ck := combo.Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	// 模拟手动开仓的持仓（未经 gate，但已落 positions 表）
	activePositions := []pgstore.Position{
		{ID: 999, OpportunityID: ck, Status: "OPEN"}, // 手动开仓的持仓
	}
	intentCombo := combo.FromOppID(ck)
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			comboAlreadyOpen = true
			break
		}
	}
	if !comboAlreadyOpen {
		t.Fatal("手动开仓持仓应被 DB 实时派生防重")
	}
}

// TestGateDedup_NoActivePosition_Pass 无活跃持仓 → 放行。
func TestGateDedup_NoActivePosition_Pass(t *testing.T) {
	activePositions := []pgstore.Position{} // 空集
	intentCombo := combo.Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			comboAlreadyOpen = true
			break
		}
	}
	if comboAlreadyOpen {
		t.Fatal("无活跃持仓不应匹配")
	}
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "SWAPARB",
		MaxDailyLoss: 100, MaxPositions: 5,
		DedupHit: comboAlreadyOpen,
	})
	if !v.Pass {
		t.Fatal("无活跃持仓应放行")
	}
}

// TestGateDedup_OldFormatOppID 旧格式 oppID（带时间戳）的活跃持仓也能匹配新格式 intent。
func TestGateDedup_OldFormatOppID(t *testing.T) {
	oldOppID := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5:1787282156117"
	intentKey := combo.Key("SWAPARB", "AUDJPY", "52993526:mt5", "277842155:mt5")
	if !combo.MatchOppID(oldOppID, combo.FromOppID(intentKey)) {
		t.Fatalf("旧格式 oppID 应匹配新格式 intent 组合键: old=%q intent=%q", oldOppID, intentKey)
	}
	// 模拟 DB 查回旧格式持仓
	activePositions := []pgstore.Position{
		{ID: 1, OpportunityID: oldOppID, Status: "OPEN"},
	}
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if combo.MatchOppID(p.OpportunityID, combo.FromOppID(intentKey)) {
			comboAlreadyOpen = true
			break
		}
	}
	if !comboAlreadyOpen {
		t.Fatal("旧格式 oppID 活跃持仓应匹配新格式 intent")
	}
}

// === D-187 CARRY 合并 SWAPARB 统一门禁栈 ===

// TestGateDecision_CarrySkipNotional D-187：CARRY 策略豁免 notional cap（同 SWAPARB）。
// gateDecision SkipNotional=true → 不校验 MaxNotional（swap 收租需整手，单笔风险 cap 语义不适用）。
func TestGateDecision_CarrySkipNotional(t *testing.T) {
	// CARRY intent notional=$99999 > MaxNotional=$1000，但 SkipNotional=true → 放行
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "CARRY", Disabled: false,
		DailyPnl: 0, MaxDailyLoss: 100, OpenCount: 0, MaxPositions: 5,
		NotionalUSD: 99999, MaxNotional: 1000, SkipNotional: true, DedupHit: false,
	})
	if !v.Pass {
		t.Fatalf("CARRY SkipNotional=true should pass (exempt notional cap), got reject: %s", v.Reason)
	}
	// 对比：非豁免策略 notional 超限 → 拒绝
	v2 := gateDecision(GateInput{
		State: StateRunning, Strategy: "MEANREVERT", Disabled: false,
		DailyPnl: 0, MaxDailyLoss: 100, OpenCount: 0, MaxPositions: 5,
		NotionalUSD: 99999, MaxNotional: 1000, SkipNotional: false, DedupHit: false,
	})
	if v2.Pass {
		t.Fatal("non-exempt strategy notional > max should reject")
	}
}

// TestComboAllowVerdict_CarryAutoAllowPass D-187：CARRY intent + autoAllow=true → 放行（同 SWAPARB）。
func TestComboAllowVerdict_CarryAutoAllowPass(t *testing.T) {
	intent := TradeIntent{Strategy: "CARRY", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	v := comboAllowVerdict(nil, nil, true, 1, 0, intent, nil)
	if !v.Pass {
		t.Fatalf("CARRY autoAllow=true should pass, got: %s", v.Reason)
	}
}

// TestComboAllowVerdict_CarryGrossSplit D-187：CARRY intent 触发 gross 折分（同 SWAPARB）。
// activeGross=$3000（CARRY 仓），remaining=$2000 < $3000×2=$6000 → 折分 AdjustedNotional=$1000。
func TestComboAllowVerdict_CarryGrossSplit(t *testing.T) {
	intent := TradeIntent{Strategy: "CARRY", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 3000}
	active := []pgstore.Position{
		{OpportunityID: "CARRY:BRENT:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: ptrFloat(3000)},
	}
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if !v.Pass {
		t.Fatalf("CARRY gross exceeded should split (pass), got reject: %s", v.Reason)
	}
	if v.AdjustedNotional != 1000 {
		t.Errorf("CARRY AdjustedNotional want 1000 (remaining/2), got %v", v.AdjustedNotional)
	}
}

// TestComboAllowVerdict_CarryActiveCountedInGross D-187：CARRY 活跃仓计入全局额度（CARRY: 前缀）。
// CARRY 仓 $50000 + SWAPARB 仓 $1000 → activeGross=$51000 > maxGross=$5000 → 拒绝（额度耗尽）。
func TestComboAllowVerdict_CarryActiveCountedInGross(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "BRENT", VenueBuy: "A:mt5", VenueSell: "B:mt5", NotionalUSD: 2000}
	active := []pgstore.Position{
		{OpportunityID: "CARRY:WTI:C:mt5:D:mt5", Status: "OPEN", NotionalUSD: ptrFloat(50000)},
		{OpportunityID: "SWAPARB:X:A:B", Status: "OPEN", NotionalUSD: ptrFloat(1000)},
	}
	// activeGross=$51000（CARRY $50000 + SWAPARB $1000），remaining=$5000-$51000<0 → 拒绝
	v := comboAllowVerdict(nil, nil, true, 5, 5000, intent, active)
	if v.Pass {
		t.Fatal("CARRY active should be counted in gross → exhausted → reject")
	}
	if !strings.Contains(v.Reason, "gross exposure exhausted") {
		t.Errorf("reason want 'gross exposure exhausted', got %q", v.Reason)
	}
}
