package dashboard

import (
	"testing"

	"nox/internal/store/pgstore"
)

// TestD149_EquivSeedTargets：venue 已映射 canonical ∈ 既有 class 域→产种子行；
// 域外品种不产；venue 已有行（含 disabled）不重复产；幂等输入去重。
func TestD149_EquivSeedTargets(t *testing.T) {
	equiv := []pgstore.AssetEquivEntry{
		{AssetClass: "WTI", Canonical: "WTI", VenueType: "perp", Venue: "binance"},
		{AssetClass: "WTI", Canonical: "CLUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "NAS100", Canonical: "NAS100", VenueType: "mt5", Venue: "110660304"}, // venue 已有行
	}
	got := equivSeedTargets(
		[]string{"WTI", "XAUUSD", "NAS100", "EURUSD", "WTI"}, // EURUSD 域外、WTI 重复
		equiv, "mt5", "110660304")
	if len(got) != 2 {
		t.Fatalf("want 2 targets, got %d: %+v", len(got), got)
	}
	byClass := map[string]*pgstore.AssetEquivEntry{}
	for _, e := range got {
		byClass[e.AssetClass] = e
	}
	if e := byClass["WTI"]; e == nil || e.Canonical != "WTI" || e.VenueType != "mt5" || e.Venue != "110660304" {
		t.Fatalf("WTI target wrong: %+v", e)
	}
	if e := byClass["GOLD"]; e == nil || e.Canonical != "XAUUSD" {
		t.Fatalf("GOLD target wrong: %+v", e)
	}
}
