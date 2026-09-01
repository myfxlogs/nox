// D-183 单测：notionalPerLotUSD 币种换算——JPY 对 ÷ USDJPY，USD 直接，商品白名单，其他 fail-closed。
package autotrade

import (
	"testing"
	"time"

	"arb/internal/bus"
)

// absDiffBps 浮点比较容差。
func absDiffBps(a, b float64) float64 {
	if a < b {
		return b - a
	}
	return a - b
}

// TestD183_NotionalPerLotUSD_JPYConversion D-183：JPY 对 notionalUSD = CS × price ÷ USDJPY。
// AUDJPY CS=100000, price=100, USDJPY=150 → notionalUSD = 100000×100/150 = $66,667。
func TestD183_NotionalPerLotUSD_JPYConversion(t *testing.T) {
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", VenueType: "mt5", Bid: 150.0, Ask: 150.0, VenueSymbol: "USDJPY", Time: time.Now()})
	got, ok := notionalPerLotUSD("AUDJPY", 100000, 100.0, qbus)
	if !ok {
		t.Fatal("want ok, got false")
	}
	want := 100000.0 * 100.0 / 150.0 // $66,667
	if absDiffBps(got, want) > 1.0 {
		t.Errorf("JPY conversion: want %.2f, got %.2f", want, got)
	}
}

// TestD183_NotionalPerLotUSD_USDDirect D-183：USD 计价直接 CS × price。
func TestD183_NotionalPerLotUSD_USDDirect(t *testing.T) {
	got, ok := notionalPerLotUSD("AUDUSD", 100000, 0.65, bus.New())
	if !ok {
		t.Fatal("USD direct: want ok, got false")
	}
	want := 100000.0 * 0.65 // $65,000
	if absDiffBps(got, want) > 0.01 {
		t.Errorf("USD direct: want %.2f, got %.2f", want, got)
	}
}

// TestD183_NotionalPerLotUSD_JPYNoFXFailClosed D-183：JPY 对无 USDJPY 报价 → fail-closed。
func TestD183_NotionalPerLotUSD_JPYNoFXFailClosed(t *testing.T) {
	_, ok := notionalPerLotUSD("AUDJPY", 100000, 100.0, bus.New())
	if ok {
		t.Error("JPY no USDJPY: want fail-closed (ok=false), got ok=true")
	}
}

// TestD183_NotionalPerLotUSD_CommodityWhitelist D-183：商品白名单 USD 计价。
func TestD183_NotionalPerLotUSD_CommodityWhitelist(t *testing.T) {
	got, ok := notionalPerLotUSD("BRENT", 1000, 80.0, bus.New())
	if !ok {
		t.Fatal("BRENT whitelist: want ok, got false")
	}
	want := 1000.0 * 80.0 // $80,000
	if absDiffBps(got, want) > 0.01 {
		t.Errorf("BRENT: want %.2f, got %.2f", want, got)
	}
}

// TestD183_NotionalPerLotUSD_UnsupportedFailClosed D-183：未支持计价币 → fail-closed。
func TestD183_NotionalPerLotUSD_UnsupportedFailClosed(t *testing.T) {
	_, ok := notionalPerLotUSD("EURGBP", 100000, 0.85, bus.New())
	if ok {
		t.Error("EURGBP unsupported: want fail-closed, got ok=true")
	}
}
