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

// fakeFP 测试用 FactProvider：只提供 swap facts。
type fakeFP struct {
	swap map[string]map[string][2]float64
}

func (f fakeFP) FundingFacts() map[string]map[string]float64 { return nil }
func (f fakeFP) SwapFacts() map[string]map[string][2]float64 { return f.swap }

// TestCarryScan 正息差产候选（年化百分点口径，D-036）、负息差/缺 Fact 不产。
func TestCarryScan(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"a:mt5": {Symbol: "EURUSD", Venue: "a", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
			"b:mt5": {Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
		},
	}
	fp := fakeFP{swap: map[string]map[string][2]float64{
		"EURUSD": {"a": {10, -5}, "b": {2, -8}}, // [0]=long, [1]=short（bps_daily）
	}}
	d := &CarryDetector{MinNetAnnualized: 4.0, FreshnessTTL: 60 * time.Second}
	cands := d.Scan(context.Background(), snap, fp)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	c := cands[0]
	// 买 a(long=10) 卖 b(short=-8)：日净 = 2 bps → 年化 = 2×365/100 = 7.3 百分点
	if c.VenueBuy != "a:mt5" || c.VenueSell != "b:mt5" {
		t.Fatalf("pair = %s→%s, want a:mt5→b:mt5", c.VenueBuy, c.VenueSell)
	}
	if c.GrossBps < 7.2 || c.GrossBps > 7.4 {
		t.Fatalf("GrossBps = %v, want ~7.3（年化百分点）", c.GrossBps)
	}
}

func TestCarryScanNegativeAndMissing(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"a:mt5": {Symbol: "EURUSD", Venue: "a", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
			"b:mt5": {Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
		},
		"GBPUSD": { // 只给 a 的 Fact → b 缺 → 不产
			"a:mt5": {Symbol: "GBPUSD", Venue: "a", VenueType: "mt5", Bid: 1.3, Ask: 1.3, Time: now},
			"b:mt5": {Symbol: "GBPUSD", Venue: "b", VenueType: "mt5", Bid: 1.3, Ask: 1.3, Time: now},
		},
	}
	fp := fakeFP{swap: map[string]map[string][2]float64{
		"EURUSD": {"a": {10, -5}, "b": {2, -30}}, // b short 太负 → 双向日净均 < 0
		"GBPUSD": {"a": {10, -5}},                // b 缺 Fact
	}}
	d := &CarryDetector{MinNetAnnualized: 4.0, FreshnessTTL: 60 * time.Second}
	cands := d.Scan(context.Background(), snap, fp)
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates（负息差+缺 Fact），got %d", len(cands))
	}
}
