// d155_pulse_test.go —— D-155 方案A：展示侧新鲜度分档锚测（三档判定表驱）。
package dashboard

import (
	"testing"
	"time"

	"nox/internal/data/bus"
)

func TestD155_QuoteFreshTiers(t *testing.T) {
	ttl := 120 * time.Second
	now := time.Now()
	qb := bus.New()
	fresh := bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 1, Ask: 2, Time: now.Add(-30 * time.Second)}
	stalePulse := bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 1, Ask: 2, Time: now.Add(-5 * time.Minute)}
	ancient := bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 1, Ask: 2, Time: now.Add(-40 * time.Minute)}
	qb.Touch("XAUUSDT", "okx:perp") // 脉冲鲜

	if !quoteFreshForDisplay(qb, fresh, ttl, now) {
		t.Fatal("窗内报价应 fresh")
	}
	if !quoteFreshForDisplay(qb, stalePulse, ttl, now) {
		t.Fatal("超窗+脉冲鲜+≤hardCap 应 fresh-extended")
	}
	// 脉冲死（另一键）→ stale
	if quoteFreshForDisplay(qb, bus.Quote{Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp",
		Bid: 1, Ask: 2, Time: now.Add(-5 * time.Minute)}, ttl, now) {
		t.Fatal("脉冲死键应 stale")
	}
	// 超 hardCap 脉冲再鲜也 stale
	if quoteFreshForDisplay(qb, ancient, ttl, now) {
		t.Fatal("超 hardCap 应 stale")
	}
	// qbus nil=无脉冲源 → 保守 stale（原语义）
	if quoteFreshForDisplay(nil, stalePulse, ttl, now) {
		t.Fatal("qbus nil 应 stale")
	}
}
