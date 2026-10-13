package lifecycle

import "testing"

// TestEvaluateElimination_NoTrades 不足 5 笔不禁用。
func TestEvaluateElimination_NoTrades(t *testing.T) {
	disable, _ := EvaluateElimination(EliminationInput{TotalTrades: 3, WinRate: 0.1, AvgPnl: -10}, seedCrits())
	if disable {
		t.Fatal("want no disable with <5 trades")
	}
}

// TestEvaluateElimination_AlreadyDisabled 已禁用不重复判定。
func TestEvaluateElimination_AlreadyDisabled(t *testing.T) {
	disable, _ := EvaluateElimination(EliminationInput{TotalTrades: 10, WinRate: 0.1, AvgPnl: -10, Disabled: true}, seedCrits())
	if disable {
		t.Fatal("want no disable (already disabled)")
	}
}

// TestEvaluateElimination_LowWinRate 胜率<30% 禁用（大样本≥30 单条件可信，D-136 判据分级）。
func TestEvaluateElimination_LowWinRate(t *testing.T) {
	disable, reason := EvaluateElimination(EliminationInput{TotalTrades: 30, WinRate: 0.2, AvgPnl: 1}, seedCrits())
	if !disable {
		t.Fatal("want disable on winRate<0.3 (large sample)")
	}
	if reason == "" {
		t.Fatal("want non-empty reason")
	}
}

// TestEvaluateElimination_NegativeAvgPnl avg_pnl<0 禁用（大样本≥30 单条件可信，D-136 判据分级）。
func TestEvaluateElimination_NegativeAvgPnl(t *testing.T) {
	disable, _ := EvaluateElimination(EliminationInput{TotalTrades: 30, WinRate: 0.5, AvgPnl: -1}, seedCrits())
	if !disable {
		t.Fatal("want disable on avgPnl<0 (large sample)")
	}
}

// TestEvaluateElimination_Healthy 健康策略不禁用。
func TestEvaluateElimination_Healthy(t *testing.T) {
	disable, _ := EvaluateElimination(EliminationInput{TotalTrades: 10, WinRate: 0.6, AvgPnl: 5}, seedCrits())
	if disable {
		t.Fatal("want no disable on healthy strategy")
	}
}

// seedCrits 判据种子行=迁移 1009 同语义（测试复刻，证明改行=改行为的载体）。
func seedCrits() []Criterion {
	return []Criterion{
		{Pred: "small_sample_kill", Params: map[string]any{"trades_gte": 5.0, "trades_lt": 30.0, "mode": "all", "conds": []any{"median_pnl<0", "avg_pnl<0"}}},
		{Pred: "large_sample_kill", Params: map[string]any{"trades_gte": 30.0, "mode": "any", "conds": []any{"win_rate<0.3", "avg_pnl<0"}}},
	}
}
