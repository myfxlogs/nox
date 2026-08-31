// engine_swaparb_skew_test.go —— D-159 W4a R7 S4：两腿 quote skew 门禁单测。
package autotrade

import (
	"testing"
	"time"

	"arb/internal/bus"
)

// TestLegSpreadTooWide_LegSpreadTooWide D-159 W4a R7 S4：long spread=60 bps > 50 → skip。
func TestLegSpreadTooWide_LegSpreadTooWide(t *testing.T) {
	qLong := bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100.00, Ask: 100.60, Time: time.Now()}  // spread=60 bps
	qShort := bus.Quote{Symbol: "AUDJPY", Venue: "vB", Bid: 100.00, Ask: 100.10, Time: time.Now()} // spread=10 bps
	tooWide, reason := legSpreadTooWide(qLong, qShort, 50.0)
	if !tooWide {
		t.Error("long spread=60 bps > 50: want tooWide=true, got false")
	}
	if reason == "" {
		t.Error("tooWide=true should have non-empty reason")
	}
}

// TestLegSpreadTooWide_BothLegsOk D-159 W4a R7 S4：long=30, short=40, max=50 → 通过。
func TestLegSpreadTooWide_BothLegsOk(t *testing.T) {
	qLong := bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100.00, Ask: 100.30, Time: time.Now()}  // spread=30 bps
	qShort := bus.Quote{Symbol: "AUDJPY", Venue: "vB", Bid: 100.00, Ask: 100.40, Time: time.Now()} // spread=40 bps
	tooWide, _ := legSpreadTooWide(qLong, qShort, 50.0)
	if tooWide {
		t.Error("long=30, short=40, max=50: want tooWide=false, got true")
	}
}

// TestLegSpreadTooWide_Disabled D-159 W4a R7 S4：maxBps=0 → 不校验（返回 false）。
func TestLegSpreadTooWide_Disabled(t *testing.T) {
	qLong := bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100.00, Ask: 100.99, Time: time.Now()}  // spread=99 bps（过宽但门禁关）
	qShort := bus.Quote{Symbol: "AUDJPY", Venue: "vB", Bid: 100.00, Ask: 100.99, Time: time.Now()} // spread=99 bps
	tooWide, reason := legSpreadTooWide(qLong, qShort, 0)
	if tooWide {
		t.Error("maxBps=0 (disabled): want tooWide=false, got true")
	}
	if reason != "" {
		t.Errorf("maxBps=0 (disabled): want empty reason, got %q", reason)
	}
}
