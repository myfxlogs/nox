// engine_position_swap_deviation_cs_test.go —— D-165.1 S2：notional 补 ContractSize 乘法单测。
// CS 注入从 service 路径（死路径）改为 cfg.LegContractSizeResolver；
// AUDJPY 为 JPY 对 → 需 e.quotes 注入 USDJPY 报价让 usdnotional.PerLot ok。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/risk/gate"
)

// newCSResolver 构造 (venueKey,venueSymbol)→CS 的 resolver（D-190 R1：cfg 路径注入）。
func newCSResolver(csMap map[string]float64) func(venueKey, venueSymbol string) (float64, bool) {
	return func(venueKey, venueSymbol string) (float64, bool) {
		if cs, ok := csMap[venueKey]; ok {
			return cs, true
		}
		return 0, false
	}
}

// newJPYQuotes 构造带 USDJPY 报价的 QuoteBus（AUDJPY 腿 PerLot 用，USDJPY=150）。
func newJPYQuotes() *bus.QuoteBus {
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "USDJPY", Bid: 149.9, Ask: 150.1, Time: time.Now()})
	return qb
}

// TestRecalcNetSwap_CS100_DeviationHigh_UseBrokerActual D-165.1 S2：MT 腿 CS=100 + 偏差>50% → 用 broker actual。
// notional 改 usdnotional.PerLot（JPY 对 ÷ USDJPY）。
// CS=100, price=100, USDJPY=150 → usdPerLot=100×100/150≈66.67, notional=1×66.67=66.67。
// actual: buy=-2.21, sell=0.5, net=-1.71 USD。actualBps=-1.71/66.67×10000≈-256.5。
// facts=0.5 bps。偏差 |0.5-(-256.5)|/max(0.5,256.5,0.01)*100 ≈ 99.8% > 50% → 用 actual（负）。
func TestRecalcNetSwap_CS100_DeviationHigh_UseBrokerActual(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{
		store: s, quotes: newJPYQuotes(),
		cfg: gate.Config{SwapDeviationExitPct: 50.0, LegContractSizeResolver: newCSResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100})},
	}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	if got >= 0 {
		t.Errorf("CS=100 high deviation: got %.4f, want negative (broker actual)", got)
	}
}

// TestRecalcNetSwap_CS100_DeviationLow_UseFacts D-165.1 S2：MT 腿 CS=100 + 偏差<50% → 用 facts。
// 不加 quotes → PerLot false → brokerActualNetSwapBps 回退 (0,false) → recalcNetSwap 用 facts=0.5。
// 语义从「偏差低用 facts」变为「PerLot 不可得回退 facts」，断言不变（want=0.5），验证回退路径。
func TestRecalcNetSwap_CS100_DeviationLow_UseFacts(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, 0.15, 0.15)
	e := &Engine{
		store: s, // 无 quotes → PerLot false → 回退 facts
		cfg:   gate.Config{SwapDeviationExitPct: 50.0, LegContractSizeResolver: newCSResolver(map[string]float64{"vA:mt5": 100, "vB:mt5": 100})},
	}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	want := 0.5 // facts 规格值（PerLot false 回退）
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("CS=100 PerLot false: got %.4f, want ~%.4f (facts fallback)", got, want)
	}
}

// TestRecalcNetSwap_CS1_OriginalBehavior D-165.1 S2 反证：CS=1 → 原 D-165 行为不变。
// CS=1, USDJPY=150 → usdPerLot=100/150≈0.667, notional=0.667。
// actual: buy=-2.21, sell=0.5, net=-1.71。actualBps=-1.71/0.667×10000≈-25650。
// facts=0.5。偏差 > 50% → 用 actual（负）。
func TestRecalcNetSwap_CS1_OriginalBehavior(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{
		store: s, quotes: newJPYQuotes(),
		cfg: gate.Config{SwapDeviationExitPct: 50.0, LegContractSizeResolver: newCSResolver(map[string]float64{"vA:mt5": 1, "vB:mt5": 1})},
	}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	if got >= 0 {
		t.Errorf("CS=1: got %.4f, want negative (original behavior, broker actual)", got)
	}
}

// TestRecalcNetSwap_ServiceNil_FallbackCS1 D-165.1 S2 反证：cfg 不注入 resolver → 回退 CS=1（D-165 原行为）。
// cfg.LegContractSizeResolver=nil → CS=1 回退；仍需 quotes（USDJPY）让 PerLot ok。
// usdPerLot=0.667, notional=0.667, actualBps=-25650 → 用 actual（负）。
func TestRecalcNetSwap_ServiceNil_FallbackCS1(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{store: s, quotes: newJPYQuotes(), cfg: gate.Config{SwapDeviationExitPct: 50.0}} // resolver=nil
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	// cfg nil 回退 CS=1，notional=0.667，actualBps=-25650 → 用 actual
	if got >= 0 {
		t.Errorf("service nil: got %.4f, want negative (fallback CS=1, broker actual)", got)
	}
}
