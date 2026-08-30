package exec

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeFxResolver 测试用 FxResolver（D-159 W1a R1 S2.3 返修）。
type fakeFxResolver struct {
	rates map[string]float64 // quoteCcy → USDPer
	ok    bool
}

func (f fakeFxResolver) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	if !f.ok {
		return 0, false
	}
	r, ok := f.rates[quoteCcy]
	if !ok {
		return 0, false
	}
	return r, true
}

// TestCheckSwapArbComboAllow_GrossRealNotional D-159 W1a R1 S2.3 返修：真实名义额 qty×CS×price×fx。
// 覆盖：CS 缺失 fail-closed / FX 缺失 fail-closed / 真实名义额超限 / 通过。
func TestCheckSwapArbComboAllow_GrossRealNotional(t *testing.T) {
	allow := []ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5", MaxGrossExposureUSD: 10000},
	}
	legs := []LegSpec{
		{VenueKey: "A:mt5", VenueSymbol: "XAUUSD", Side: "BUY", Qty: 0.06, PriceExpected: 2500},
		{VenueKey: "B:mt5", VenueSymbol: "XAUUSD", Side: "SELL", Qty: 0.06, PriceExpected: 2500},
	}
	// XAUUSD: 0.06 × CS=100 × 2500 × fx=1 = $15000/leg，两腿 $30000 > $10000 限。

	// case 1: CS 缺失 → fail-closed
	p := &Pipeline{
		swapArbComboAllow: allow,
		csResolver:        &fakeCSResolver{sizes: map[string]float64{}}, // 无 CS
		fxResolver:        fakeFxResolver{rates: map[string]float64{"USD": 1}, ok: true},
	}
	err := p.checkSwapArbComboAllow(context.Background(), "SWAPARB:XAUUSD:A:mt5:B:mt5", legs)
	if !errors.Is(err, ErrComboNotAllowed) {
		t.Errorf("CS missing: want ErrComboNotAllowed, got %v", err)
	}

	// case 2: FX 缺失（非 USD 计价）→ fail-closed
	// 用 JPY 计价 canonical（XAUJPY）触发 fx 路径
	jpyLegs := []LegSpec{
		{VenueKey: "A:mt5", VenueSymbol: "XAUJPY", Side: "BUY", Qty: 0.06, PriceExpected: 2500},
		{VenueKey: "B:mt5", VenueSymbol: "XAUJPY", Side: "SELL", Qty: 0.06, PriceExpected: 2500},
	}
	jpyAllow := []ComboAllowSpec{
		{Canonical: "XAUJPY", VenueLong: "A:mt5", VenueShort: "B:mt5", MaxGrossExposureUSD: 10000},
	}
	p = &Pipeline{
		swapArbComboAllow: jpyAllow,
		csResolver:        &fakeCSResolver{sizes: map[string]float64{"A:mt5:XAUJPY": 100, "B:mt5:XAUJPY": 100}},
		fxResolver:        fakeFxResolver{ok: false}, // FX 不可得
	}
	err = p.checkSwapArbComboAllow(context.Background(), "SWAPARB:XAUJPY:A:mt5:B:mt5", jpyLegs)
	if !errors.Is(err, ErrComboNotAllowed) {
		t.Errorf("FX missing: want ErrComboNotAllowed, got %v", err)
	}

	// case 3: 真实名义额超限 → ErrGrossExposureExceeds
	p = &Pipeline{
		swapArbComboAllow: allow,
		csResolver:        &fakeCSResolver{sizes: map[string]float64{"A:mt5:XAUUSD": 100, "B:mt5:XAUUSD": 100}},
		fxResolver:        fakeFxResolver{rates: map[string]float64{"USD": 1}, ok: true},
	}
	err = p.checkSwapArbComboAllow(context.Background(), "SWAPARB:XAUUSD:A:mt5:B:mt5", legs)
	if !errors.Is(err, ErrGrossExposureExceeds) {
		t.Errorf("gross exceed: want ErrGrossExposureExceeds, got %v", err)
	}

	// case 4: 真实名义额未超限 → 通过（提高上限到 $50000）
	passAllow := []ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5", MaxGrossExposureUSD: 50000},
	}
	p = &Pipeline{
		swapArbComboAllow: passAllow,
		csResolver:        &fakeCSResolver{sizes: map[string]float64{"A:mt5:XAUUSD": 100, "B:mt5:XAUUSD": 100}},
		fxResolver:        fakeFxResolver{rates: map[string]float64{"USD": 1}, ok: true},
	}
	err = p.checkSwapArbComboAllow(context.Background(), "SWAPARB:XAUUSD:A:mt5:B:mt5", legs)
	if err != nil {
		t.Errorf("gross within limit: want nil, got %v", err)
	}
}

// TestCheckSwapArbComboAllow_CSResolverNotWired D-159 W1a R1 S2.3 返修：csResolver 未接线 fail-closed。
func TestCheckSwapArbComboAllow_CSResolverNotWired(t *testing.T) {
	allow := []ComboAllowSpec{
		{Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5", MaxGrossExposureUSD: 10000},
	}
	p := &Pipeline{
		swapArbComboAllow: allow,
		// csResolver nil
	}
	err := p.checkSwapArbComboAllow(context.Background(), "SWAPARB:XAUUSD:A:mt5:B:mt5",
		[]LegSpec{{VenueKey: "A:mt5", VenueSymbol: "XAUUSD", Side: "BUY", Qty: 0.06, PriceExpected: 2500}})
	if !errors.Is(err, ErrComboNotAllowed) {
		t.Errorf("csResolver not wired: want ErrComboNotAllowed, got %v", err)
	}
}
