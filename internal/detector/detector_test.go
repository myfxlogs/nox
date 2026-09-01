package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

func TestCrossVenueScan(t *testing.T) {
	now := time.Now()
	d := &CrossVenueDetector{CexMinGrossBps: 5.0, CfdMinGrossBps: 5.0, FreshnessTTL: 60 * time.Second}
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

// TestCrossMinBpsFor crossMinBpsFor 三态：全 CFD→cfd；全 CEX→cex；混合/未知→max（保守）。
func TestCrossMinBpsFor(t *testing.T) {
	const cex, cfd = 10.0, 2.0
	cases := []struct {
		name       string
		venueTypes []string
		want       float64
	}{
		{name: "all CFD", venueTypes: []string{"mt4", "mt5"}, want: cfd},
		{name: "all CEX", venueTypes: []string{"spot", "perp"}, want: cex},
		{name: "mixed", venueTypes: []string{"mt5", "spot"}, want: cex}, // max(10, 2) = 10
		{name: "unknown", venueTypes: []string{""}, want: cex},          // 未知类型归 max(10, 2) = 10
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crossMinBpsFor(tc.venueTypes, cex, cfd); got != tc.want {
				t.Fatalf("crossMinBpsFor(%v) = %v, want %v", tc.venueTypes, got, tc.want)
			}
		})
	}
}

// TestCrossVenueScanDualThreshold 同量级 gross≈8.18bps：CEX 门槛 10 → 不产候选；CFD 门槛 2 → 产候选。
func TestCrossVenueScanDualThreshold(t *testing.T) {
	now := time.Now()
	d := &CrossVenueDetector{CexMinGrossBps: 10.0, CfdMinGrossBps: 2.0, FreshnessTTL: 60 * time.Second}

	cexSnap := map[string]map[string]bus.Quote{
		"BTC": {
			"binance:spot": {Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 1.1000, Ask: 1.1001, Time: now},
			"okx:spot":     {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 1.1010, Ask: 1.1011, Time: now},
		},
	}
	// spread = 1.1010-1.1001 = 0.0009, mid ≈ 1.10005, gross ≈ 8.18 bps
	if cands := d.Scan(context.Background(), cexSnap, nil); len(cands) != 0 {
		t.Fatalf("CEX legs: expected 0 candidates (gross 8.18 < 10), got %d", len(cands))
	}

	cfdSnap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"icmarkets:mt4": {Symbol: "EURUSD", Venue: "icmarkets", VenueType: "mt4", Bid: 1.1000, Ask: 1.1001, Time: now},
			"exness:mt4":    {Symbol: "EURUSD", Venue: "exness", VenueType: "mt4", Bid: 1.1010, Ask: 1.1011, Time: now},
		},
	}
	if cands := d.Scan(context.Background(), cfdSnap, nil); len(cands) == 0 {
		t.Fatal("CFD legs: expected candidates (gross 8.18 > 2), got 0")
	}
}

func TestCrossVenueStaleExcluded(t *testing.T) {
	now := time.Now().Add(-2 * time.Minute) // 过期
	d := &CrossVenueDetector{CexMinGrossBps: 1.0, CfdMinGrossBps: 1.0, FreshnessTTL: 60 * time.Second}
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

// fakeFP 测试用 FactProvider：提供 swap + trade_mode facts。
type fakeFP struct {
	swap   map[string]map[string][2]float64
	modes  map[string]map[string]int64
	isReal map[string]*bool // D-178
}

func (f fakeFP) FundingFacts() map[string]map[string]float64 { return nil }
func (f fakeFP) SwapFacts() map[string]map[string][2]float64 { return f.swap }
func (f fakeFP) TradeModes() map[string]map[string]int64     { return f.modes }
func (f fakeFP) IsRealMap() map[string]*bool                 { return f.isReal } // D-178

// TestCrossVenueScanMixedLegs D-056：同 canonical BTCUSDT 下 mt5(CFD)+okx(CEX) 双腿 →
// 混合门槛 max(cex=10, cfd=2)=10bps；gross>10 触发候选，gross<10 不触发。
// 验证 canonical 统一映射解除 CFD-CEX cross 结构性盲区（D-035 遗留）。
func TestCrossVenueScanMixedLegs(t *testing.T) {
	now := time.Now()
	d := &CrossVenueDetector{CexMinGrossBps: 10.0, CfdMinGrossBps: 2.0, FreshnessTTL: 60 * time.Second}

	// gross ≈ 12.72 bps > 混合门槛 10 → 触发
	hot := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"mt5:mt5":  {Symbol: "BTCUSDT", Venue: "mt5", VenueType: "mt5", Bid: 1.1000, Ask: 1.1001, Time: now},
			"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 1.1015, Ask: 1.1016, Time: now},
		},
	}
	cands := d.Scan(context.Background(), hot, nil)
	if len(cands) == 0 {
		t.Fatal("mixed legs gross≈12.72 > 10: expected candidates, got 0")
	}
	found := false
	for _, c := range cands {
		if c.VenueBuy == "mt5:mt5" && c.VenueSell == "okx:spot" {
			if c.GrossBps <= 10.0 {
				t.Fatalf("GrossBps = %v, want > 10.0", c.GrossBps)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected mt5→okx mixed-leg candidate")
	}

	// gross ≈ 8.18 bps < 混合门槛 10 → 不触发（诚实抑制，非缺陷）
	cold := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"mt5:mt5":  {Symbol: "BTCUSDT", Venue: "mt5", VenueType: "mt5", Bid: 1.1000, Ask: 1.1001, Time: now},
			"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 1.1010, Ask: 1.1011, Time: now},
		},
	}
	if cands := d.Scan(context.Background(), cold, nil); len(cands) != 0 {
		t.Fatalf("mixed legs gross≈8.18 < 10: expected 0 candidates, got %d", len(cands))
	}
}

// TestCrossVenueScanTradeModeFilter D-060 F4：cross 检测器同口径——禁交易方向不产候选，
// 未知放行；方向区分（LongOnly 腿可当买腿不可当卖腿）。
func TestCrossVenueScanTradeModeFilter(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"XAUUSD": {
			"a:mt5": {Symbol: "XAUUSD", Venue: "a", VenueType: "mt5", Bid: 1.1000, Ask: 1.1001, Time: now},
			"b:mt5": {Symbol: "XAUUSD", Venue: "b", VenueType: "mt5", Bid: 1.1015, Ask: 1.1016, Time: now},
		},
	}
	d := &CrossVenueDetector{CexMinGrossBps: 2.0, CfdMinGrossBps: 2.0, FreshnessTTL: 60 * time.Second}
	// 基线：唯一候选 = 买 a 卖 b（gross≈12.7bps；买 b 卖 a 为负）。
	if cands := d.Scan(context.Background(), snap, nil); len(cands) != 1 {
		t.Fatalf("基线：want 1 candidate, got %d", len(cands))
	}
	modes := map[string]map[string]int64{"XAUUSD": {"a:mt5": fact.TradeModeFull, "b:mt5": fact.TradeModeFull}}
	// 买腿 a 禁交易 → 0。
	modes["XAUUSD"]["a:mt5"] = fact.TradeModeDisabled
	if cands := d.Scan(context.Background(), snap, fakeFP{modes: modes}); len(cands) != 0 {
		t.Fatalf("买腿 Disabled：want 0, got %d", len(cands))
	}
	// 买腿 a 只可开多：买 a 合法（方向区分）→ 1。
	modes["XAUUSD"]["a:mt5"] = fact.TradeModeLongOnly
	if cands := d.Scan(context.Background(), snap, fakeFP{modes: modes}); len(cands) != 1 {
		t.Fatalf("买腿 LongOnly：want 1, got %d", len(cands))
	}
	// 买腿 a 只可开空：买 a 非法 → 0。
	modes["XAUUSD"]["a:mt5"] = fact.TradeModeShortOnly
	if cands := d.Scan(context.Background(), snap, fakeFP{modes: modes}); len(cands) != 0 {
		t.Fatalf("买腿 ShortOnly：want 0, got %d", len(cands))
	}
	// 卖腿 b 只可开多：卖 b 非法 → 0。
	modes["XAUUSD"]["a:mt5"] = fact.TradeModeFull
	modes["XAUUSD"]["b:mt5"] = fact.TradeModeLongOnly
	if cands := d.Scan(context.Background(), snap, fakeFP{modes: modes}); len(cands) != 0 {
		t.Fatalf("卖腿 LongOnly：want 0, got %d", len(cands))
	}
	// 卖腿 b 只可开空 → 1。
	modes["XAUUSD"]["b:mt5"] = fact.TradeModeShortOnly
	if cands := d.Scan(context.Background(), snap, fakeFP{modes: modes}); len(cands) != 1 {
		t.Fatalf("卖腿 ShortOnly：want 1, got %d", len(cands))
	}
}
