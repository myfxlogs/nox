package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestXAssetCarryWTICrossVenue D-308 S5：asset_equiv 新类（WTI 四成员=
// CLUSDT@binance + CL-USDT-SWAP@okx + CL_USDT@gate + WTI@3001157:mt5）配对自动展开
// C(4,2)=6 对各一候选，含 perp-perp 跨所对（CL_USDT→CLUSDT 卖高息收 funding 差）。
// funding map：CLUSDT/binance=8.0、CL-USDT-SWAP/okx=5.0、CL_USDT/gate=2.0。
func TestXAssetCarryWTICrossVenue(t *testing.T) {
	now := time.Now()
	equiv := fakeEquiv{byClass: map[string][]AssetEquivEntry{"WTI": {
		{AssetClass: "WTI", Canonical: "CLUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "WTI", Canonical: "CL-USDT-SWAP", VenueType: "perp", Venue: "okx"},
		{AssetClass: "WTI", Canonical: "CL_USDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "WTI", Canonical: "WTI", VenueType: "mt5", Venue: "3001157"},
	}}}
	mkQ := func(sym, venue, vt string) map[string]bus.Quote {
		return map[string]bus.Quote{venue + ":" + vt: {
			Symbol: sym, Venue: venue, VenueType: vt, Bid: 70.0, Ask: 70.02, Time: now,
		}}
	}
	snap := map[string]map[string]bus.Quote{
		"CLUSDT":       mkQ("CLUSDT", "binance", "perp"),
		"CL-USDT-SWAP": mkQ("CL-USDT-SWAP", "okx", "perp"),
		"CL_USDT":      mkQ("CL_USDT", "gate", "perp"),
		"WTI":          mkQ("WTI", "3001157", "mt5"),
	}
	fp := fakeFP{funding: map[string]map[string]float64{
		"CLUSDT":       {"binance": 8.0},
		"CL-USDT-SWAP": {"okx": 5.0},
		"CL_USDT":      {"gate": 2.0},
	}}
	d := newXAssetDetector()
	d.AssetEquiv = equiv
	cands := d.Scan(context.Background(), snap, fp)
	if len(cands) != 6 {
		t.Fatalf("want 6 candidates (C(4,2)=6 pairs), got %d: %+v", len(cands), cands)
	}
	pairs := make(map[string]int)
	var crossVenue, mt5Leg *Candidate
	for i := range cands {
		c := &cands[i]
		a, b := c.Canonical, c.CanonicalSell
		if a > b {
			a, b = b, a
		}
		pairs[a+"|"+b]++
		if c.Canonical == "CL_USDT" && c.CanonicalSell == "CLUSDT" {
			crossVenue = c
		}
		if c.Canonical == "WTI" && c.CanonicalSell == "CLUSDT" {
			mt5Leg = c
		}
	}
	if len(pairs) != 6 {
		t.Fatalf("distinct canonical pairs want 6, got %d: %v", len(pairs), pairs)
	}
	for p, n := range pairs {
		if n != 1 {
			t.Fatalf("pair %s produced %d candidates, want 1", p, n)
		}
	}
	// perp-perp 跨所对：卖 CLUSDT（funding 8.0）买 CL_USDT（funding 2.0）→ 毛息差 6.0。
	if crossVenue == nil {
		t.Fatalf("missing CL_USDT→CLUSDT cross-venue candidate: %+v", cands)
	}
	if crossVenue.VenueBuy != "gate:perp" || crossVenue.VenueSell != "binance:perp" {
		t.Fatalf("cross-venue venues want gate:perp→binance:perp, got %s→%s", crossVenue.VenueBuy, crossVenue.VenueSell)
	}
	if crossVenue.GrossBps < 5.9 || crossVenue.GrossBps > 6.1 {
		t.Fatalf("CL_USDT→CLUSDT GrossBps want ~6.0 (8.0−2.0), got %v", crossVenue.GrossBps)
	}
	if crossVenue.AssetClass != "WTI" {
		t.Fatalf("AssetClass want WTI, got %s", crossVenue.AssetClass)
	}
	// mt5 腿方向：买 WTI@3001157 卖 CLUSDT 收 funding 8.0。
	if mt5Leg == nil {
		t.Fatalf("missing WTI→CLUSDT candidate: %+v", cands)
	}
	if mt5Leg.VenueBuy != "3001157:mt5" || mt5Leg.GrossBps < 7.9 || mt5Leg.GrossBps > 8.1 {
		t.Fatalf("WTI→CLUSDT want buy=3001157:mt5 gross~8.0, got %s %v", mt5Leg.VenueBuy, mt5Leg.GrossBps)
	}
}
