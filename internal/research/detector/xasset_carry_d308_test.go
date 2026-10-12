package detector

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
)

// TestXAssetCarryWTICrossVenue D-308 S5 + 0101 勘误后语义：canonical 全所同键
// <base>USDT，WTI 四成员=CLUSDT@{binance,okx,gate} + WTI@3001157:mt5 →
// byCanon 两组（CLUSDT 三 venue 行同组作备选报价）→ 仅 CLUSDT↔WTI 一对，
// 卖腿按 venue 展开 3 个候选（gross=各所 funding：8.0/5.0/2.0）。
// 同 canonical 跨所 funding 对（CLUSDT@gate↔CLUSDT@binance）不属本 detector——
// 是 CrossVenueDetector 领地。
func TestXAssetCarryWTICrossVenue(t *testing.T) {
	now := time.Now()
	equiv := fakeEquiv{byClass: map[string][]AssetEquivEntry{"WTI": {
		{AssetClass: "WTI", Canonical: "CLUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "WTI", Canonical: "CLUSDT", VenueType: "perp", Venue: "okx"},
		{AssetClass: "WTI", Canonical: "CLUSDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "WTI", Canonical: "WTI", VenueType: "mt5", Venue: "3001157"},
	}}}
	snap := map[string]map[string]bus.Quote{
		"CLUSDT": {
			"binance:perp": {Symbol: "CLUSDT", Venue: "binance", VenueType: "perp", Bid: 70.0, Ask: 70.02, Time: now},
			"okx:perp":     {Symbol: "CLUSDT", Venue: "okx", VenueType: "perp", Bid: 70.0, Ask: 70.02, Time: now},
			"gate:perp":    {Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 70.0, Ask: 70.02, Time: now},
		},
		"WTI": {
			"3001157:mt5": {Symbol: "WTI", Venue: "3001157", VenueType: "mt5", Bid: 70.0, Ask: 70.02, Time: now},
		},
	}
	fp := fakeFP{funding: map[string]map[string]float64{
		"CLUSDT": {"binance": 8.0, "okx": 5.0, "gate": 2.0},
	}}
	d := newXAssetDetector()
	d.AssetEquiv = equiv
	cands := d.Scan(context.Background(), snap, fp)
	if len(cands) != 3 {
		t.Fatalf("want 3 candidates (CLUSDT↔WTI × 3 venue sell legs), got %d: %+v", len(cands), cands)
	}
	want := map[string]float64{"binance:perp": 8.0, "okx:perp": 5.0, "gate:perp": 2.0}
	for _, c := range cands {
		if c.Canonical != "WTI" || c.CanonicalSell != "CLUSDT" {
			t.Fatalf("candidate want WTI→CLUSDT, got %s→%s", c.Canonical, c.CanonicalSell)
		}
		if c.VenueBuy != "3001157:mt5" {
			t.Fatalf("VenueBuy want 3001157:mt5, got %s", c.VenueBuy)
		}
		g, ok := want[c.VenueSell]
		if !ok {
			t.Fatalf("unexpected VenueSell %s", c.VenueSell)
		}
		if c.GrossBps < g-0.1 || c.GrossBps > g+0.1 {
			t.Fatalf("VenueSell %s GrossBps want ~%v, got %v", c.VenueSell, g, c.GrossBps)
		}
		if c.AssetClass != "WTI" {
			t.Fatalf("AssetClass want WTI, got %s", c.AssetClass)
		}
		delete(want, c.VenueSell)
	}
	if len(want) != 0 {
		t.Fatalf("missing venue sell legs: %v", want)
	}
}
