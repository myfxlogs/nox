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
	swap  map[string]map[string][2]float64
	modes map[string]map[string]int64
}

func (f fakeFP) FundingFacts() map[string]map[string]float64 { return nil }
func (f fakeFP) SwapFacts() map[string]map[string][2]float64 { return f.swap }
func (f fakeFP) TradeModes() map[string]map[string]int64     { return f.modes }

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

// TestCarryScanTradeModeFilter D-060 F4：禁交易腿不产候选；未知（无 Fact）放行。
// swap 息差同 TestCarryScan（买 a 卖 b 为唯一正对），trade_mode 变化只应消灭非法腿的候选。
func TestCarryScanTradeModeFilter(t *testing.T) {
	now := time.Now()
	snap := map[string]map[string]bus.Quote{
		"EURUSD": {
			"a:mt5": {Symbol: "EURUSD", Venue: "a", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
			"b:mt5": {Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.1, Ask: 1.1, Time: now},
		},
	}
	swap := map[string]map[string][2]float64{
		"EURUSD": {"a": {10, -5}, "b": {2, -8}}, // 买 a 卖 b = 唯一正对（~7.3%）
	}
	d := &CarryDetector{MinNetAnnualized: 4.0, FreshnessTTL: 60 * time.Second}

	// 买腿 a 禁交易（PXBT BTCUSD 实证形态）→ 不产候选。
	fp := fakeFP{swap: swap, modes: map[string]map[string]int64{
		"EURUSD": {"a:mt5": fact.TradeModeDisabled, "b:mt5": fact.TradeModeFull},
	}}
	if cands := d.Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Fatalf("买腿 Disabled：want 0 candidates, got %d", len(cands))
	}

	// 卖腿 b 只可开多（ShortOnly 之外）→ 卖腿非法 → 不产。
	fp.modes = map[string]map[string]int64{
		"EURUSD": {"a:mt5": fact.TradeModeFull, "b:mt5": fact.TradeModeLongOnly},
	}
	if cands := d.Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Fatalf("卖腿 LongOnly：want 0 candidates, got %d", len(cands))
	}

	// 腿模式合法（买 LongOnly 卖 ShortOnly）→ 照常产候选。
	fp.modes = map[string]map[string]int64{
		"EURUSD": {"a:mt5": fact.TradeModeLongOnly, "b:mt5": fact.TradeModeShortOnly},
	}
	if cands := d.Scan(context.Background(), snap, fp); len(cands) != 1 {
		t.Fatalf("合法腿：want 1 candidate, got %d", len(cands))
	}

	// 未知（无 trade_mode Fact）→ 放行（冷启动期不空白化矩阵）。
	fp.modes = nil
	if cands := d.Scan(context.Background(), snap, fp); len(cands) != 1 {
		t.Fatalf("未知放行：want 1 candidate, got %d", len(cands))
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
