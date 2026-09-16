package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// fakeEquiv 测试用 AssetEquivSource（裁 5：detector 不查库，注入内存视图）。
type fakeEquiv struct {
	byClass map[string][]AssetEquivEntry
}

func (f fakeEquiv) ByClass() map[string][]AssetEquivEntry { return f.byClass }

// goldEquiv v1 种子形状：GOLD = PAXGUSDT@binance:perp + XAUUSD@3001157:mt5。
var goldEquiv = fakeEquiv{byClass: map[string][]AssetEquivEntry{
	"GOLD": {
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "binance"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
	},
}}

func goldSnap(now time.Time) map[string]map[string]bus.Quote {
	return map[string]map[string]bus.Quote{
		"PAXGUSDT": {
			"binance:perp": {Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now},
		},
		"XAUUSD": {
			"3001157:mt5": {Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now},
		},
	}
}

func newXAssetDetector() *XAssetCarryDetector {
	return &XAssetCarryDetector{
		EntryMinBps:     -10, // 容忍小幅逆价进场
		MinNetAnnualPct: 1.0,
		TrackingMaxBps:  50,
		FreshnessTTL:    60 * time.Second,
		AssetEquiv:      goldEquiv,
	}
}

// TestXAssetCarryScan 主路径：买 XAU@3001157 卖 PAXG perp，funding 8.0 收空腿 → 净息差 8.0。
func TestXAssetCarryScan(t *testing.T) {
	now := time.Now()
	fp := fakeFP{funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}}}
	cands := newXAssetDetector().Scan(context.Background(), goldSnap(now), fp)
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	c := cands[0]
	if c.Type != TypeXAssetCarry {
		t.Fatalf("type want XASSET_CARRY, got %s", c.Type)
	}
	if c.Canonical != "XAUUSD" || c.CanonicalSell != "PAXGUSDT" || c.AssetClass != "GOLD" {
		t.Fatalf("canonical/asset_class want XAUUSD→PAXGUSDT@GOLD, got %s→%s@%s", c.Canonical, c.CanonicalSell, c.AssetClass)
	}
	if c.VenueBuy != "3001157:mt5" || c.VenueSell != "binance:perp" {
		t.Fatalf("venues want 3001157:mt5→binance:perp, got %s→%s", c.VenueBuy, c.VenueSell)
	}
	if c.GrossBps < 7.9 || c.GrossBps > 8.1 {
		t.Fatalf("GrossBps want ~8.0, got %v", c.GrossBps)
	}
	if c.PriceBuy != 4000.0 || c.PriceSell != 4000.0 {
		t.Fatalf("prices want buy=4000.0 sell=4000.0, got %v/%v", c.PriceBuy, c.PriceSell)
	}
}

// TestXAssetCarrySwapCost swap 负值（付息）扣减净息差：funding 8.0 + swap_long −1.0bps/d（≈−3.65%/年）→ net≈4.35。
func TestXAssetCarrySwapCost(t *testing.T) {
	now := time.Now()
	fp := fakeFP{
		funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}},
		swap:    map[string]map[string][2]float64{"XAUUSD": {"3001157": {-1.0, 0}}},
	}
	cands := newXAssetDetector().Scan(context.Background(), goldSnap(now), fp)
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
	if want := 8.0 - 3.65; cands[0].GrossBps < want-0.1 || cands[0].GrossBps > want+0.1 {
		t.Fatalf("GrossBps want ~%.2f (funding−swap), got %v", want, cands[0].GrossBps)
	}
	// swap 吃掉净息差至门槛以下 → 不产候选
	fp.swap["XAUUSD"]["3001157"] = [2]float64{-2.0, 0} // −7.3%/年 → net≈0.7 < 1.0
	if cands := newXAssetDetector().Scan(context.Background(), goldSnap(now), fp); len(cands) != 0 {
		t.Fatalf("net below MinNetAnnualPct: want 0, got %d", len(cands))
	}
}

// TestXAssetCarryTrackingGate 跟踪误差超门槛（mid 偏离 >50bps）不产候选。
func TestXAssetCarryTrackingGate(t *testing.T) {
	now := time.Now()
	snap := goldSnap(now)
	snap["PAXGUSDT"]["binance:perp"] = bus.Quote{
		Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp",
		Bid: 4021.0, Ask: 4021.5, Time: now, // mid 偏离 XAU ~54bps
	}
	fp := fakeFP{funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}}}
	if cands := newXAssetDetector().Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Fatalf("tracking >50bps: want 0, got %d", len(cands))
	}
}

// TestXAssetCarryEntryGate 进场价差逆价超 EntryMinBps 不产候选。
func TestXAssetCarryEntryGate(t *testing.T) {
	now := time.Now()
	snap := goldSnap(now)
	snap["PAXGUSDT"]["binance:perp"] = bus.Quote{
		Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp",
		Bid: 3999.0, Ask: 3999.5, Time: now, // sell bid 3999 < buy ask 4000 → entry≈−2.5bps > −10 仍过
	}
	fp := fakeFP{funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}}}
	if cands := newXAssetDetector().Scan(context.Background(), snap, fp); len(cands) != 1 {
		t.Fatalf("entry≈−2.5bps > −10: want 1, got %d", len(cands))
	}
	snap["PAXGUSDT"]["binance:perp"] = bus.Quote{
		Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp",
		Bid: 3990.0, Ask: 3990.5, Time: now, // entry≈−25bps < −10
	}
	if cands := newXAssetDetector().Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Fatalf("entry≈−25bps < −10: want 0, got %d", len(cands))
	}
}

// TestXAssetCarryStale A4 边界锚：XAU 腿过期 → 不产候选不报错。
func TestXAssetCarryStale(t *testing.T) {
	now := time.Now()
	snap := goldSnap(now)
	q := snap["XAUUSD"]["3001157:mt5"]
	q.Time = now.Add(-3 * time.Minute) // 周末/收盘 stale
	snap["XAUUSD"]["3001157:mt5"] = q
	fp := fakeFP{funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}}}
	if cands := newXAssetDetector().Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Fatalf("stale XAU leg: want 0, got %d", len(cands))
	}
}

// TestXAssetCarryNoFunding perp 腿 funding fact 缺失 → 不评估不产候选。
func TestXAssetCarryNoFunding(t *testing.T) {
	if cands := newXAssetDetector().Scan(context.Background(), goldSnap(time.Now()), fakeFP{}); len(cands) != 0 {
		t.Fatalf("funding missing: want 0, got %d", len(cands))
	}
}

// TestXAssetCarryTradeMode 裁 7：沿用 legTradable——XAU 买腿 ShortOnly → 不产候选；未知放行。
func TestXAssetCarryTradeMode(t *testing.T) {
	now := time.Now()
	fp := fakeFP{
		funding: map[string]map[string]float64{"PAXGUSDT": {"binance": 8.0}},
		modes:   map[string]map[string]int64{"XAUUSD": {"3001157:mt5": fact.TradeModeShortOnly}},
	}
	if cands := newXAssetDetector().Scan(context.Background(), goldSnap(now), fp); len(cands) != 0 {
		t.Fatalf("buy leg ShortOnly: want 0, got %d", len(cands))
	}
	fp.modes["XAUUSD"]["3001157:mt5"] = fact.TradeModeLongOnly
	if cands := newXAssetDetector().Scan(context.Background(), goldSnap(now), fp); len(cands) != 1 {
		t.Fatalf("buy leg LongOnly: want 1, got %d", len(cands))
	}
}

// TestXAssetCarryNilEquiv AssetEquiv 未注入 → 零候选零 panic。
func TestXAssetCarryNilEquiv(t *testing.T) {
	d := &XAssetCarryDetector{MinNetAnnualPct: 0, TrackingMaxBps: 50, FreshnessTTL: time.Minute}
	if cands := d.Scan(context.Background(), goldSnap(time.Now()), fakeFP{}); len(cands) != 0 {
		t.Fatalf("nil equiv: want 0, got %d", len(cands))
	}
}
