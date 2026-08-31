// engine_position_swap_deviation_cs_test.go —— D-165.1 S2：notional 补 ContractSize 乘法单测。
package autotrade

import (
	"context"
	"testing"

	"arb/internal/dashboard"
)

// newServiceWithCS 构造带 LegContractSize resolver 的 Service（最小 NewService 调用，store=nil）。
func newServiceWithCS(t *testing.T, csMap map[string]float64) *dashboard.Service {
	t.Helper()
	svc := dashboard.New(nil, nil, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, nil, 0, nil, nil, 0)
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		if cs, ok := csMap[venueKey]; ok {
			return cs, true
		}
		return 0, false
	})
	return svc
}

// TestRecalcNetSwap_CS100_DeviationHigh_UseBrokerActual D-165.1 S2：MT 腿 CS=100 + 偏差>50% → 用 broker actual。
// notional = qty(1) × price(100) × CS(100) = 10000。actual: buy=-2.21, sell=0.5, net=-1.71 USD。
// actualBps = -1.71/10000*10000 = -1.71。facts=0.5 bps。偏差 |0.5-(-1.71)|/max(0.5,1.71,0.01)*100 ≈ 129% > 50% → 用 actual。
func TestRecalcNetSwap_CS100_DeviationHigh_UseBrokerActual(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	svc := newServiceWithCS(t, map[string]float64{"vA:mt5": 100, "vB:mt5": 100})
	e := &Engine{store: s, service: svc, cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	// actualBps = (-2.21 + 0.5) / 10000 * 10000 = -1.71
	if got >= 0 {
		t.Errorf("CS=100 high deviation: got %.4f, want negative (broker actual)", got)
	}
}

// TestRecalcNetSwap_CS100_DeviationLow_UseFacts D-165.1 S2：MT 腿 CS=100 + 偏差<50% → 用 facts。
// notional=10000, actual: buy=0.0024, sell=0.0024, net=0.0048。actualBps=0.0048/10000*10000=0.0048。
// facts=0.5。偏差 |0.5-0.0048|/max(0.5,0.0048,0.01)*100 = 0.4952/0.5*100 ≈ 99% > 50% → 仍用 actual。
// 需更接近：actual=0.48 bps → netUSD=0.0048*10000/10000=0.48... 改用 actual=0.49 → netUSD=0.000049*10000=0.49
// 简化：用 actual 让偏差<50%——actual=0.3 bps → netUSD=0.3*10000/10000=0.3, buy=0.15, sell=0.15
// 偏差 |0.5-0.3|/max(0.5,0.3,0.01)*100 = 0.2/0.5*100 = 40% < 50% → 用 facts。
func TestRecalcNetSwap_CS100_DeviationLow_UseFacts(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, 0.15, 0.15) // actualBps=0.3
	svc := newServiceWithCS(t, map[string]float64{"vA:mt5": 100, "vB:mt5": 100})
	e := &Engine{store: s, service: svc, cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	want := 0.5 // facts 规格值
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("CS=100 low deviation: got %.4f, want ~%.4f (facts)", got, want)
	}
}

// TestRecalcNetSwap_CS1_OriginalBehavior D-165.1 S2 反证：CS=1 → 原 D-165 行为不变。
// notional = qty(1) × price(100) × CS(1) = 100。actual: buy=-2.21, sell=0.5, net=-1.71。
// actualBps = -1.71/100*10000 = -171。facts=0.5。偏差 > 50% → 用 actual（-171）。
func TestRecalcNetSwap_CS1_OriginalBehavior(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	svc := newServiceWithCS(t, map[string]float64{"vA:mt5": 1, "vB:mt5": 1})
	e := &Engine{store: s, service: svc, cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	// CS=1 → notional=100 → actualBps=-171（同 D-165 TestRecalcNetSwap_DeviationHigh_UseBrokerActual）
	if got >= 0 {
		t.Errorf("CS=1: got %.4f, want negative (original behavior, broker actual)", got)
	}
}

// TestRecalcNetSwap_ServiceNil_FallbackCS1 D-165.1 S2 反证：service=nil → 回退 CS=1（D-165 原行为）。
// e.service=nil → resolver 未注入 → notional=qty×price=100（无 CS）。actualBps=-171。facts=0.5 → 用 actual。
func TestRecalcNetSwap_ServiceNil_FallbackCS1(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{store: s, service: nil, cfg: Config{SwapDeviationExitPct: 50.0}} // service=nil
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	// service=nil 回退 CS=1，notional=100，actualBps=-171 → 用 actual
	if got >= 0 {
		t.Errorf("service nil: got %.4f, want negative (fallback CS=1, broker actual)", got)
	}
}
