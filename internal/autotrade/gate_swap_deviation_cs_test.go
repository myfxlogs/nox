// gate_swap_deviation_cs_test.go —— D-165.1 S1：notional 补 ContractSize 乘法单测。
package autotrade

import (
	"context"
	"strings"
	"testing"
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
	g := NewGate(s, Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := TradeIntent{
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
	g := NewGate(s, Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := TradeIntent{
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
	g := NewGate(s, Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 1, "vB:mt5": 1}),
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := TradeIntent{
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

// TestSwapDeviationGate_ResolverNil_FallbackCS1 D-165.1 S1 反证：resolver=nil → 回退 CS=1（D-165 原行为）。
// notional = qty(0.01) × price(100) = 1（无 CS）。facts=0 → factsUSD=0。actual=-2.21 → 偏差 100% → 拒绝。
func TestSwapDeviationGate_ResolverNil_FallbackCS1(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21)
	g := NewGate(s, Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: nil, // 未注入
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute:  func(ctx context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
	}
	res := g.Submit(context.Background(), &intent)
	// resolver=nil 回退 CS=1，notional=1，facts=0, actual=-2.21 → 偏差 100% → 仍拒绝
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("resolver nil: got %s/%s, want REJECTED (fallback CS=1, high deviation)", res.Status, res.Reason)
	}
}

// TestSwapDeviationGate_ResolverOkFalse_RejectsFailClosed D-165.1/D-190 R2：
// resolver 注入但 ok=false → CS=1 回退 → PerLot ok（有 USDJPY）→ notional 低估 →
// factsUSD≈0 vs actual=-2.21 → deviation≈100% > 50% → REJECT（fail-closed，复审认可）。
// 旧语义（跳过腿放行）是审计定性的 fail-open 洞，D-190 修复后 CEX 腿偏差检查首次激活。
func TestSwapDeviationGate_ResolverOkFalse_RejectsFailClosed(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21) // facts=0, actual=-2.21 → 高偏差
	g := NewGate(s, Config{
		MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0,
		LegContractSizeResolver: csResolver(map[string]float64{}), // 空 map → 全部 ok=false → CS=1 回退
		Quotes:                  setupSwapDeviationQuotes(),
	}, nil)
	intent := TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (fail-closed reject)")
			return "FAILED", "", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	// CS=1 回退 → PerLot ok → notional 低估 → facts=0 vs actual=-2.21 → deviation≈100% → REJECT
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("resolver ok=false: got %s/%s, want REJECTED/swap deviation gate (fail-closed)", res.Status, res.Reason)
	}
}
