package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// D-178：四检测器 isRealPair 集成测试——MIXED is_real → 0 候选；SAME is_real → 候选照常。
// fakeIsRealFP 构造带 isReal 的 FactProvider（复用 fakeFP/fakeFactProvider 不便跨文件，
// 此处内联一个最小 fake 满足四检测器所需 facts）。
type fakeIsRealFP struct {
	funding map[string]map[string]float64
	swap    map[string]map[string][2]float64
	isReal  map[string]*bool
}

func (f *fakeIsRealFP) FundingFacts() map[string]map[string]float64 { return f.funding }
func (f *fakeIsRealFP) SwapFacts() map[string]map[string][2]float64 { return f.swap }
func (f *fakeIsRealFP) TradeModes() map[string]map[string]int64     { return nil }
func (f *fakeIsRealFP) IsRealMap() map[string]*bool                 { return f.isReal }

// TestCrossVenueScanIsRealPair D-178：MIXED is_real → 0 候选；SAME is_real → 候选照常。
func TestCrossVenueScanIsRealPair(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"a:mt5": {Symbol: "BTCUSDT", Venue: "a", VenueType: "mt5", Bid: 1.1200, Ask: 1.1000, Time: now},
			"b:mt5": {Symbol: "BTCUSDT", Venue: "b", VenueType: "mt5", Bid: 1.1000, Ask: 1.0800, Time: now},
		},
	}
	d := &CrossVenueDetector{CexMinGrossBps: 2.0, CfdMinGrossBps: 2.0, FreshnessTTL: 60 * time.Second}

	tr, fa := true, false

	// MIXED → 0 候选
	mixedFP := &fakeIsRealFP{isReal: map[string]*bool{"a": &tr, "b": &fa}}
	if cands := d.Scan(context.Background(), snap, mixedFP); len(cands) != 0 {
		t.Fatalf("MIXED is_real: want 0 candidates, got %d", len(cands))
	}

	// SAME → 候选照常
	sameFP := &fakeIsRealFP{isReal: map[string]*bool{"a": &tr, "b": &tr}}
	if cands := d.Scan(context.Background(), snap, sameFP); len(cands) == 0 {
		t.Fatalf("SAME is_real: want candidates, got 0")
	}
}

// TestPerpFundingScanIsRealPair D-178：MIXED is_real → 0 候选；SAME is_real → 候选照常。
func TestPerpFundingScanIsRealPair(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:perp":  {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 99990, Ask: 100000, Time: now},
		},
	}
	funding := map[string]map[string]float64{
		"BTCUSDT": {"okx": 6.10, "gate": -1.31},
	}
	d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}

	tr, fa := true, false

	// MIXED → 0 候选
	mixedFP := &fakeIsRealFP{funding: funding, isReal: map[string]*bool{"okx": &tr, "gate": &fa}}
	if cands := d.Scan(context.Background(), snap, mixedFP); len(cands) != 0 {
		t.Fatalf("MIXED is_real: want 0 candidates, got %d", len(cands))
	}

	// SAME (both demo) → 候选照常
	sameFP := &fakeIsRealFP{funding: funding, isReal: map[string]*bool{"okx": &fa, "gate": &fa}}
	if cands := d.Scan(context.Background(), snap, sameFP); len(cands) == 0 {
		t.Fatalf("SAME is_real (both demo): want candidates, got 0")
	}
}

// TestFundingHedgeScanIsRealPair D-178：MIXED is_real → 0 候选；SAME is_real → 候选照常。
func TestFundingHedgeScanIsRealPair(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
			"okx:perp": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
		},
	}
	funding := map[string]map[string]float64{
		"BTCUSDT": {"okx": 37.0},
	}
	d := &FundingHedgeDetector{MinNetAnnualized: 4.0, BaselinePct: 10.95, FreshnessTTL: 30 * time.Second}

	tr, fa := true, false

	// spot+perp 同 venue "okx" → isRealPair 同 venue 必 SAME，用不同 venue 测 MIXED
	snapMixed := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:spot":  {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
		},
	}
	fundingMixed := map[string]map[string]float64{
		"BTCUSDT": {"gate": 37.0},
	}

	// MIXED → 0 候选
	mixedFP := &fakeIsRealFP{funding: fundingMixed, isReal: map[string]*bool{"okx": &tr, "gate": &fa}}
	if cands := d.Scan(context.Background(), snapMixed, mixedFP); len(cands) != 0 {
		t.Fatalf("MIXED is_real: want 0 candidates, got %d", len(cands))
	}

	// SAME → 候选照常（同 venue okx）
	sameFP := &fakeIsRealFP{funding: funding, isReal: map[string]*bool{"okx": &tr}}
	if cands := d.Scan(context.Background(), snap, sameFP); len(cands) != 1 {
		t.Fatalf("SAME is_real: want 1 candidate, got %d", len(cands))
	}
}
