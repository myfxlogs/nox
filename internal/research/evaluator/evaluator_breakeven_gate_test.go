// evaluator_breakeven_gate_test.go —— D-133 T2：息差型回本周期上门单测（先红后绿）。
// 两用例：⑥breakevenDays=2.0 > 门 1.0 → NetBps 为负 ⑦breakevenDays=0.5 → 原值。
package evaluator

import (
	"testing"
	"time"

	"nox/internal/research/detector"
)

// breakevenGateCfg D-133 T2 测试配置：FundingMaxBreakevenDays=1.0。
// 其余参数与 baseCfg 同（margin 4.5、slippage 1.0、grab 4.5）。
func breakevenGateCfg() Config {
	return Config{
		SlippageBps:             1.0,
		MarginRateAnnual:        4.5,
		DetectorFreshnessTTL:    60 * time.Second,
		OpportunityTTL:          30 * time.Second,
		GrabMinNetAnnualized:    4.5,
		FundingMaxBreakevenDays: 1.0, // D-133 T2 回本门
	}
}

// ⑥ breakevenDays=2.0 > 门 1.0 → NetBps 为负（trap，下游 funding_min% 自然淘汰）。
// 构造：carry gross=5.0 → net=0.5%；spread+commission+slippage 故意放大使 breakevenDays≈2.0。
// dailyNetBps = 0.5/100×10000/365 ≈ 0.137；oneTimeCost = (spread+comm+slip)×2。
// 要 bd=2.0 → oneTimeCost = 0.137×2 = 0.274 → spread+comm+slip = 0.137。
// 用 spreadBuy=0.0345+spreadSell=0.0345+commission=0.034+slippage=2.0 → 实际 slippage 占大头，
// 简化：直接用大 spread 使 bd>1.0 即可——spreadBuy=spreadSell=0.5, comm=0.6, slip=1.0
// → oneTime=(1.0+0.6+2.0)×2=7.2; dailyNet=0.137; bd=7.2/0.137≈52.5 >> 1.0 → netBps=-1。
func TestEvaluateBreakevenGate_OverLimit_NetNegative(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  5.0, // net = 5.0 - 4.5 = 0.5%（正但小）
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, breakevenGateCfg(), 0.5, 0.5, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.NetBps >= 0 {
		t.Fatalf("⑥ over-limit: NetBps = %v, want < 0 (breakevenDays > 1.0 → 置负淘汰)", r.NetBps)
	}
	if r.Rating != RatingTrap {
		t.Fatalf("⑥ over-limit: Rating = %s, want trap (net≤0 恒 trap)", r.Rating)
	}
	if r.BreakevenDays == nil || *r.BreakevenDays <= 1.0 {
		t.Fatalf("⑥ over-limit: BreakevenDays = %v, want > 1.0 (前置条件)", r.BreakevenDays)
	}
}

// ⑦ breakevenDays=0.5 ≤ 门 1.0 → 原值（不被置负，正常评级）。
// 构造：carry gross=10.0 → net=5.5%；spread+comm+slip 小 → bd≈0.5。
// dailyNetBps = 5.5/100×10000/365 ≈ 1.507；要 bd=0.5 → oneTime=0.754 → spread+comm+slip=0.377。
// 用 spreadBuy=spreadSell=0.01, comm=0.05, slip=1.0 → oneTime=(0.02+0.05+2.0)×2=4.14; bd=4.14/1.507≈2.75 > 1.0 ❌
// 改用 gross=20.0 → net=15.5%; dailyNet=15.5/100×10000/365≈4.247; oneTime=4.14; bd=4.14/4.247≈0.975 ≈<1.0 ✓
// 但 0.975 接近边界——用 gross=30.0 → net=25.5; dailyNet=25.5/100×10000/365≈6.986; bd=4.14/6.986≈0.59 ✓
func TestEvaluateBreakevenGate_WithinLimit_OriginalNet(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  30.0, // net = 30.0 - 4.5 = 25.5%
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, breakevenGateCfg(), 0.01, 0.01, 0.05, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.BreakevenDays == nil || *r.BreakevenDays > 1.0 {
		t.Fatalf("⑦ within-limit: BreakevenDays = %v, want ≤ 1.0 (前置条件)", r.BreakevenDays)
	}
	// net 应保持原值 25.5（不被置负）
	if r.NetBps < 25.0 || r.NetBps > 26.0 {
		t.Fatalf("⑦ within-limit: NetBps = %v, want ~25.5 (原值不被置负)", r.NetBps)
	}
	if r.Rating == RatingTrap {
		t.Fatalf("⑦ within-limit: Rating = trap, want grab/breakeven (net>0 正常评级)")
	}
}
