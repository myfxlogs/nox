package autotrade

import "testing"

// D-136 修复①：evaluateElimination 判据分级（样本量分级）。
// <30 笔（小样本噪音保护）：胜率<30% 且 avgPnl<0 → 禁用
// ≥30 笔（大样本，大数定律）：胜率<30% 或 avgPnl<0 任一 → 禁用
// 先红后绿：用例①在旧代码（avgPnl<0 单条件即杀）上=红。

// TestEvaluateElimination_D136_PerfFundingForm 7 笔 winRate=0.43 avg=-0.196（PERP_FUNDING 形态）
// → 不禁（小样本+胜率达标，avg 微负=噪音）。旧代码禁=红。
func TestEvaluateElimination_D136_PerfFundingForm(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 7, WinRate: 0.43, AvgPnl: -0.196})
	if disable {
		t.Fatal("want no disable: 7 trades <30 floor, winRate=0.43>=0.3 → 小样本噪音保护")
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

// TestEvaluateElimination_D136_SmallSampleExtreme 5 笔 winRate=0.0 avg<0（小样本极端）→ 禁。
func TestEvaluateElimination_D136_SmallSampleExtreme(t *testing.T) {
	disable, _ := evaluateElimination(EliminationInput{TotalTrades: 5, WinRate: 0.0, AvgPnl: -1.0})
	if !disable {
		t.Fatal("want disable: 5 trades <30, winRate=0.0<0.3 且 avgPnl<0 → 双条件满足")
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
