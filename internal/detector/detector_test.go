package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

func TestCrossVenueScan(t *testing.T) {
	now := time.Now()
	d := &CrossVenueDetector{MinGrossBps: 5.0, FreshnessTTL: 60 * time.Second}
	snap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"icmarkets:mt4": {Symbol: "EURUSD", Venue: "icmarkets", VenueType: "mt4", Bid: 1.1000, Ask: 1.1001, Time: now},
			"exness:mt4":    {Symbol: "EURUSD", Venue: "exness", VenueType: "mt4", Bid: 1.1010, Ask: 1.1011, Time: now},
		},
	}
	cands := d.Scan(context.Background(), snap, nil)
	if len(cands) == 0 {
		t.Fatal("expected candidates, got 0")
	}
	// 买 icmarkets(ask=1.1001) 卖 exness(bid=1.1010): spread = 0.0009, bps = 0.0009/1.10005*10000 ≈ 8.18
	found := false
	for _, c := range cands {
		if c.VenueBuy == "icmarkets:mt4" && c.VenueSell == "exness:mt4" {
			if c.GrossBps < 5.0 {
				t.Fatalf("GrossBps = %v, want > 5.0", c.GrossBps)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected icmarkets→exness candidate")
	}
}

func TestCrossVenueStaleExcluded(t *testing.T) {
	now := time.Now().Add(-2 * time.Minute) // 过期
	d := &CrossVenueDetector{MinGrossBps: 1.0, FreshnessTTL: 60 * time.Second}
	snap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"a:mt4": {Symbol: "EURUSD", Venue: "a", VenueType: "mt4", Bid: 1.0, Ask: 1.0, Time: now},
			"b:mt4": {Symbol: "EURUSD", Venue: "b", VenueType: "mt4", Bid: 2.0, Ask: 2.0, Time: now},
		},
	}
	cands := d.Scan(context.Background(), snap, nil)
	if len(cands) != 0 {
		t.Fatalf("expected 0 stale candidates, got %d", len(cands))
	}
}

func TestDedup(t *testing.T) {
	cands := []Candidate{
		{Type: TypeCrossVenue, Canonical: "X", VenueBuy: "a:mt4", VenueSell: "b:mt4", GrossBps: 10},
		{Type: TypeCrossVenue, Canonical: "X", VenueBuy: "b:mt4", VenueSell: "a:mt4", GrossBps: 12},
	}
	out := dedup(cands)
	if len(out) != 1 {
		t.Fatalf("dedup len = %d, want 1", len(out))
	}
	if out[0].GrossBps != 12 {
		t.Fatalf("GrossBps = %v, want 12 (keep best)", out[0].GrossBps)
	}
}
