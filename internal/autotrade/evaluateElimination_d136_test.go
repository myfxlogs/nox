package autotrade

import "testing"

// D-136 修复①：evaluateElimination 判据分级（样本量分级）。
// D-154 S2 修订：小样本判据 winRate→median（median<0 AND avg<0 才杀，winRate 移出）。
// <30 笔（小样本噪音保护）：medianPnl<0 且 avgPnl<0 → 禁用
// ≥30 笔（大样本，大数定律）：胜率<30% 或 avgPnl<0 任一 → 禁用
// 先红后绿：用例①在旧代码（avgPnl<0 单条件即杀）上=红。

// TestEvaluateElimination_D136_PerfFundingForm 7 笔 winRate=0.43 avg=-0.196 median=0（PERP_FUNDING 形态）
// → 不禁（小样本，median=0 不<0 → D-154 S2 双条件不满足）。旧代码禁=红。
// D-154 S2：winRate 移出小样本判据，median 占位 0 = 不杀（与裁决①语义一致）。
func TestEvaluateElimination_D136_PerfFundingForm(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 7, WinRate: 0.43, AvgPnl: -0.196, MedianPnl: 0})
	if disable {
		t.Fatal("want no disable: 7 trades <30 floor, median=0 不<0 → D-154 S2 双条件不满足")
	}
}

// TestEvaluateElimination_D136_MeanRevertForm 75 笔 winRate=0.0 avg<0（MEANREVERT 形态）→ 禁（回归）。
func TestEvaluateElimination_D136_MeanRevertForm(t *testing.T) {
	disable, reason := evaluateElimination(EliminationInput{TotalTrades: 75, WinRate: 0.0, AvgPnl: -0.5})
	if !disable {
		t.Fatal("want disable: 75 trades >=30, winRate=0.0<0.3")
	}
	if reason == "" {
		t.Fatal("want non-empty reason")
	}
}

// TestEvaluateElimination_D136_LargeSampleNegAvg 40 笔 winRate=0.6 avg=-0.05（大样本微亏）→ 禁（≥30 均值可信）。
func TestEvaluateElimination_D136_LargeSampleNegAvg(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 40, WinRate: 0.6, AvgPnl: -0.05})
	if !disable {
		t.Fatal("want disable: 40 trades >=30, avgPnl<0 → 大样本均值可信")
	}
}

// TestEvaluateElimination_D136_SmallSampleExtreme 5 笔 winRate=0.0 avg=-1.0 median=-1.0（小样本极端双负）→ 禁。
// D-154 S2：小样本判据改 median+avg 双负（winRate 移出），加 median=-1.0 保持「极端负杀」原意。
func TestEvaluateElimination_D136_SmallSampleExtreme(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 5, WinRate: 0.0, AvgPnl: -1.0, MedianPnl: -1.0})
	if !disable {
		t.Fatal("want disable: 5 trades <30, median=-1.0<0 且 avgPnl=-1.0<0 → D-154 S2 双条件满足")
	}
}

// TestEvaluateElimination_D136_SmallSampleHealthy 8 笔 winRate=0.6 avg=+0.1 → 不禁（回归）。
func TestEvaluateElimination_D136_SmallSampleHealthy(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 8, WinRate: 0.6, AvgPnl: 0.1})
	if disable {
		t.Fatal("want no disable: healthy small sample")
	}
}

// TestEvaluateElimination_D136_BelowMinTrades 3 笔 → 不禁（<5 回归）。
func TestEvaluateElimination_D136_BelowMinTrades(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 3, WinRate: 0.0, AvgPnl: -10})
	if disable {
		t.Fatal("want no disable: <5 trades")
	}
}

// D-154 S5：evaluateElimination v2 补充用例。
// 小样本 median+avg 双负杀 / median 正 avg 负不杀（误杀保护锚）。

// TestEvaluateElimination_D154_MedianNegAvgNegKill 7 笔 median=-0.5 avg=-0.3（双负）→ 杀。
// PERP_FUNDING 实证形态：多数仓净负，median 暴露收益结构真相。
func TestEvaluateElimination_D154_MedianNegAvgNegKill(t *testing.T) {
	disable, reason := evaluateElimination(EliminationInput{TotalTrades: 7, WinRate: 0.33, AvgPnl: -0.3, MedianPnl: -0.5})
	if !disable {
		t.Fatal("want disable: 7 trades <30, median=-0.5<0 且 avg=-0.3<0 → D-154 S2 双条件满足")
	}
	if reason == "" {
		t.Fatal("want non-empty reason")
	}
}

// TestEvaluateElimination_D154_MedianPosAvgNegNoKill 7 笔 median=+0.1 avg=-0.05（median 正 avg 负）→ 不杀。
// D-136 误杀保护锚：多数仓净正（median>0），少数极端负拉低 avg → 不杀。
// 旧 winRate 双条件（winRate=0.6>=0.3）也不杀，但 S2 后 winRate 移出——median 正才是新判据的保护点。
func TestEvaluateElimination_D154_MedianPosAvgNegNoKill(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 7, WinRate: 0.6, AvgPnl: -0.05, MedianPnl: 0.1})
	if disable {
		t.Fatal("want no disable: 7 trades <30, median=+0.1 不<0 → D-154 S2 双条件不满足（误杀保护）")
	}
}

// TestEvaluateElimination_D154_LargeSampleWinRateKill 40 笔 winRate=0.2 avg=+1（大样本 winRate 单条件）→ 杀。
func TestEvaluateElimination_D154_LargeSampleWinRateKill(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 40, WinRate: 0.2, AvgPnl: 1.0, MedianPnl: 0.5})
	if !disable {
		t.Fatal("want disable: 40 trades >=30, winRate=0.2<0.3 → 大样本 winRate 单条件杀")
	}
}

// TestEvaluateElimination_D154_LargeSampleAvgKill 40 笔 winRate=0.6 avg=-0.5（大样本 avg 单条件）→ 杀。
func TestEvaluateElimination_D154_LargeSampleAvgKill(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 40, WinRate: 0.6, AvgPnl: -0.5, MedianPnl: 0.1})
	if !disable {
		t.Fatal("want disable: 40 trades >=30, avg=-0.5<0 → 大样本 avg 单条件杀")
	}
}

// TestEvaluateElimination_D154_ZeroTrades 0 笔 → 不杀（<5 零副作用）。
func TestEvaluateElimination_D154_ZeroTrades(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 0, WinRate: 0, AvgPnl: 0, MedianPnl: 0})
	if disable {
		t.Fatal("want no disable: 0 trades <5 零副作用")
	}
}

// TestEvaluateElimination_D154_DisabledNoop D-154 返修②②：已禁用零副作用。
// Disabled=true + 极端负样本（100 笔 winRate=0 avg=-100 median=-100）→ 首行短路，
// disable=false 且 reason=""（禁用态零副作用，不重复禁用/不产 reason 噪音）。
func TestEvaluateElimination_D154_DisabledNoop(t *testing.T) {
	disable, reason := evaluateElimination(EliminationInput{Disabled: true, TotalTrades: 100, WinRate: 0, AvgPnl: -100, MedianPnl: -100})
	if disable {
		t.Fatal("want no disable: Disabled=true 首行短路，极端负样本也不杀")
	}
	if reason != "" {
		t.Errorf("reason=%q, want \"\"（禁用态零副作用，不产 reason 噪音）", reason)
	}
}
