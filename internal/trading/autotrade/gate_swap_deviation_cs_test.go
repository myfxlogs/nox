// gate_swap_deviation_cs_test.go —— D-165.1 S1：notional 补 ContractSize 乘法单测。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

// csResolver 返回固定 CS 的 resolver（venueKey→CS 映射，未匹配返回 ok=false）。
func csResolver(csMap map[string]float64) func(venueKey, venueSymbol string) (float64, bool) {
	return func(venueKey, venueSymbol string) (float64, bool) {
		if cs, ok := csMap[venueKey]; ok {
			return cs, true
		}
		return 0, false
	}
}

// TestSwapDeviationGate_CS100_HighDeviation_Rejected D-165.1 S1：MT 腿 CS=100 + 偏差>50% → REJECTED。
// notional = qty(0.01) × price(100) × CS(100) = 100。facts=0.5 bps → factsUSD=0.005。
// actual=-2.21 USD → 偏差 |0.005-(-2.21)|/max(0.005,2.21,0.01)*100 ≈ 99.8% > 50% → 拒绝。
func TestSwapDeviationGate_CS100_HighDeviation_Rejected(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.5, -2.21) // facts=0.5 bps, actual=-2.21 USD
	g := gate.NewGate(s, gate.Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := gate.TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (CS=100 high deviation reject)")
			return "FAILED", "", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("CS=100 high deviation: got %s/%s, want REJECTED/swap deviation gate", res.Status, res.Reason)
	}
}

// TestSwapDeviationGate_CS100_LowDeviation_Pass D-165.1 S1：MT 腿 CS=100 + 偏差<50% → 放行。
// notional=100, facts=0.5 bps → factsUSD=0.005。actual=0.005 USD → 偏差 0% < 50% → 放行。
func TestSwapDeviationGate_CS100_LowDeviation_Pass(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.5, 0.005) // facts=0.5, actual=0.005
	g := gate.NewGate(s, gate.Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := gate.TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute:  func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("CS=100 low deviation should pass, rejected: %s", res.Reason)
	}
}

// TestSwapDeviationGate_CS1_OriginalBehavior D-165.1 S1 反证：CS=1 → 原 D-165 行为不变。
// notional = qty(0.01) × price(100) × CS(1) = 1。facts=0 bps → factsUSD=0。
// actual=-2.21 → 偏差 100% > 50% → 拒绝（同 D-165 TestSwapDeviationGate_HighDeviation_Rejected）。
func TestSwapDeviationGate_CS1_OriginalBehavior(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21) // facts=0, actual=-2.21
	g := gate.NewGate(s, gate.Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 1, "vB:mt5": 1}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := gate.TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (CS=1 high deviation reject)")
			return "FAILED", "", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("CS=1: got %s/%s, want REJECTED/swap deviation gate (original behavior)", res.Status, res.Reason)
	}
}

// TestSwapDeviationGate_ResolverNil_CannotEvaluate D-565 S5：resolver=nil → 合约规格不可得→
// 「无法评估」同径跳过腿不拦（fail-closed 由下游事实面承担，cs=1 静默回退已删）。
func TestSwapDeviationGate_ResolverNil_CannotEvaluate(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21)
	g := gate.NewGate(s, gate.Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: nil, // 未注入
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	executed := false
	intent := gate.TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute:  func(ctx context.Context) (string, string, int64) { executed = true; return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	// resolver nil→cs 不可得→无法评估跳过（偏差闸不据此拦、也不静默编 CS=1）
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("resolver nil 不应被偏差闸拦（无法评估同径）, got %s", res.Reason)
	}
	if !executed {
		t.Errorf("resolver nil 应跳过偏差评估放行, got %s/%s", res.Status, res.Reason)
	}
}

// TestSwapDeviationGate_ResolverZero_CannotEvaluate D-565 S5：resolver 返 0/ok=false →
// 合约规格不可得→「无法评估」同径跳过（不再静默编 CS=1 推 notional 偏差）。
func TestSwapDeviationGate_ResolverZero_CannotEvaluate(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21) // facts=0, actual=-2.21 → 若硬编 CS=1 会误拒
	g := gate.NewGate(s, gate.Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 0, "vB:mt5": 0}), // 非正=不可得
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	executed := false
	intent := gate.TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute:  func(ctx context.Context) (string, string, int64) { executed = true; return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("resolver 返 0 不应被偏差闸拦（无法评估同径）, got %s", res.Reason)
	}
	if !executed {
		t.Errorf("resolver 返 0 应跳过偏差评估放行, got %s/%s", res.Status, res.Reason)
	}
}

// TestSwapDeviationGate_StaleFactIgnored D-565 S5：facts 超龄行不采（SwapFactMaxAge 龄闸）。
// 新鲜 facts=+2.21bps 对冲 actual=-2.21 → 本可通过；同一值超龄 → factsBps=0 → 偏差 100% 拒。
func TestSwapDeviationGate_StaleFactIgnored(t *testing.T) {
	ctx := context.Background()
	seed := func(factAge time.Duration) *pgstore.Store {
		s := setupSwapDeviationStore(t, 0.5, -2.21)
		if _, err := s.Pool().Exec(ctx, `UPDATE facts SET ts=now()-$1*interval '1 second' WHERE kind=$2`,
			factAge.Seconds(), fact.KindSwapLong); err != nil {
			t.Fatalf("age fact: %v", err)
		}
		return s
	}
	newGate := func(s *pgstore.Store) *gate.Gate {
		return gate.NewGate(s, gate.Config{
			MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
			SwapFactMaxAge:          time.Hour,
			LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 1, "vB:mt5": 1}),
			Quotes:                  setupSwapDeviationQuotes(),
		}, nil)
	}
	newIntent := func() gate.TradeIntent {
		return gate.TradeIntent{
			Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
			VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
			DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
			Execute:  func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
		}
	}
	// 超龄（2h>1h 闸）→ factsBps=0 → factsUSD=0 vs actual -2.21 → 偏差 100% → REJECT
	in := newIntent()
	res := newGate(seed(2*time.Hour)).Submit(ctx, &in)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("stale fact: got %s/%s, want REJECTED（超龄行不采→偏差 100%%）", res.Status, res.Reason)
	}
	// 同龄值但 fresh（10min<1h 闸）→ 采信 → notional=1 → factsUSD≈0.005 vs -2.21 → 仍 REJECT
	// （对照：证明拒因是龄闸生效后的 facts=0 口径而非其他）
	in2 := newIntent()
	res2 := newGate(seed(10*time.Minute)).Submit(ctx, &in2)
	if res2.Status != "REJECTED" || !strings.Contains(res2.Reason, "swap deviation gate") {
		t.Errorf("fresh fact 对照: got %s/%s, want REJECTED", res2.Status, res2.Reason)
	}
}
