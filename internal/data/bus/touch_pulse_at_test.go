// d155_pulse_test.go —— D-155 方案A：QuoteBus 活性脉冲锚测。
package bus

import (
	"testing"
	"time"
)

// TestD155_TouchPulseAt 脉冲打点/读回 + EvictVenue 同驱逐 + quote.Time 零污染。
func TestD155_TouchPulseAt(t *testing.T) {
	b := New()
	qt := time.Now().Add(-10 * time.Minute)
	b.Publish(Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP",
		Bid: 4000, Ask: 4001, Time: qt})
	// 脉冲前：无脉冲
	if _, ok := b.PulseAt("XAUUSDT", "okx:perp"); ok {
		t.Fatal("未打点前有脉冲")
	}
	b.Touch("XAUUSDT", "okx:perp")
	t1, ok := b.PulseAt("XAUUSDT", "okx:perp")
	if !ok || time.Since(t1) > 5*time.Second {
		t.Fatalf("脉冲未打点: ok=%v t=%v", ok, t1)
	}
	// 脉冲不写报价值：quote.Time 仍原始 venue 时刻（eval 域零动锚）
	q, ok := b.Latest("XAUUSDT", "okx:perp")
	if !ok || !q.Time.Equal(qt) {
		t.Fatalf("脉冲污染 quote.Time: %v want %v", q.Time, qt)
	}
	// EvictVenue 同驱逐
	b.EvictVenue("okx")
	if _, ok := b.PulseAt("XAUUSDT", "okx:perp"); ok {
		t.Fatal("EvictVenue 后脉冲残留")
	}
}
