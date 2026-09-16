// xasset_integration_test.go —— D-300 S4：Engine 接入回归锚。
// G1 快照扩展（q.Symbol ∪ Partners）+ G4 findQuote 双 canonical + netStoreGate XASSET_CARRY case。
package engine

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/detector"
	"arb/internal/evaluator"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// fakeEquivView 测试用 AssetEquivView（Partners 反查 + MaybeRefresh 计数）。
type fakeEquivView struct {
	partners  map[string][]string
	refreshes int
}

func (f *fakeEquivView) Partners(c string) []string   { return f.partners[c] }
func (f *fakeEquivView) MaybeRefresh(context.Context) { f.refreshes++ }

// fakeEquivSrc 测试用 detector.AssetEquivSource。
type fakeEquivSrc struct {
	m map[string][]detector.AssetEquivEntry
}

func (f fakeEquivSrc) ByClass() map[string][]detector.AssetEquivEntry { return f.m }

// snapCapture 记录 Scan 收到的快照（验证 G1 扩展）。
type snapCapture struct {
	last map[string]map[string]bus.Quote
}

func (d *snapCapture) Type() detector.OppType { return detector.TypeCrossVenue }
func (d *snapCapture) Scan(_ context.Context, snap map[string]map[string]bus.Quote, _ detector.FactProvider) []detector.Candidate {
	d.last = snap
	return nil
}

// TestXAssetSnapshotExpansion G1：XAUUSD 报价触发 → 快照含 partners 扩展的 PAXGUSDT。
func TestXAssetSnapshotExpansion(t *testing.T) {
	qbus := bus.New()
	equiv := &fakeEquivView{partners: map[string][]string{"XAUUSD": {"PAXGUSDT"}}}
	cap := &snapCapture{}
	e := New(qbus, []detector.Detector{cap}, evaluator.Config{DetectorFreshnessTTL: time.Minute}, newFakeOppStore(), NewFactCache(), testCommissionResolver(t, nil, nil), time.Nanosecond, nil, 0, 0, 0, 0, 0)
	e.SetAssetEquiv(equiv)

	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now})
	qbus.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
	e.onQuote(context.Background(), bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now})

	if equiv.refreshes == 0 {
		t.Error("MaybeRefresh 未被调用")
	}
	if _, ok := cap.last["PAXGUSDT"]; !ok {
		t.Fatalf("快照未扩展 partner PAXGUSDT: %+v", cap.last)
	}
	if _, ok := cap.last["XAUUSD"]; !ok {
		t.Fatalf("快照缺原 canonical XAUUSD: %+v", cap.last)
	}
}

// TestXAssetEvaluateSellCanonical G4：卖腿 canonical≠买腿时 findQuote 用 CanonicalSell 解析，
// evaluate 拿到另一条 canonical 的报价（返回非 nil 且价格来自 PAXGUSDT 腿）。
func TestXAssetEvaluateSellCanonical(t *testing.T) {
	qbus := bus.New()
	now := time.Now()
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now})
	qbus.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})

	e := New(qbus, nil, evaluator.Config{MarginRateAnnual: 4.5, DetectorFreshnessTTL: time.Minute}, newFakeOppStore(), NewFactCache(), testCommissionResolver(t, nil, nil), time.Nanosecond, nil, 0, 0, 0, 0, 0)
	snap := qbus.Snapshot(context.Background(), []string{"XAUUSD", "PAXGUSDT"})
	cand := detector.Candidate{
		Type:          detector.TypeXAssetCarry,
		Canonical:     "XAUUSD",
		CanonicalSell: "PAXGUSDT",
		AssetClass:    "GOLD",
		VenueBuy:      "3001157:mt5",
		VenueSell:     "binance:perp",
		PriceBuy:      4000.0,
		PriceSell:     4000.0,
		GrossBps:      8.0,
		QuoteTime:     now.UnixMilli(),
	}
	res := e.evaluate(context.Background(), cand, snap)
	if res == nil {
		t.Fatal("evaluate returned nil — sell leg quote not resolved via CanonicalSell")
	}
	if res.VenueSell != "binance:perp" || res.PriceSell != 4000.0 {
		t.Fatalf("sell leg resolved wrong: %+v", res)
	}
}

// TestXAssetNetStoreGate G5：XASSET_CARRY 走独立入库净门槛。
func TestXAssetNetStoreGate(t *testing.T) {
	e := &Engine{xassetCarryMinNetStore: 3.0}
	if !netStoreGate(&pgstore.Opportunity{Type: "XASSET_CARRY", NetBps: 3.5}, e) {
		t.Error("net 3.5 >= 3.0 should store")
	}
	if netStoreGate(&pgstore.Opportunity{Type: "XASSET_CARRY", NetBps: 2.9}, e) {
		t.Error("net 2.9 < 3.0 should not store")
	}
	e2 := &Engine{} // 零门槛=不过滤
	if !netStoreGate(&pgstore.Opportunity{Type: "XASSET_CARRY", NetBps: -1}, e2) {
		t.Error("zero threshold should pass through")
	}
}

// TestXAssetOnQuoteStaleLeg A4：XAU 腿 stale → detector 不产候选，引擎零产出零报错。
func TestXAssetOnQuoteStaleLeg(t *testing.T) {
	qbus := bus.New()
	now := time.Now()
	stale := now.Add(-3 * time.Minute)
	// PAXG 新鲜，XAU stale（周末/收盘场景）
	qbus.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: stale})

	fc := NewFactCache()
	fc.UpdateFacts([]fact.Fact{{Kind: fact.KindFunding, Venue: "binance", Symbol: "PAXGUSDT", Value: 8.0, Ts: now}})
	d := &detector.XAssetCarryDetector{
		EntryMinBps: -20, MinNetAnnualPct: 1.0, TrackingMaxBps: 50, FreshnessTTL: time.Minute,
		AssetEquiv: fakeEquivSrc{m: map[string][]detector.AssetEquivEntry{"GOLD": {
			{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "binance"},
			{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
		}}},
	}
	equiv := &fakeEquivView{partners: map[string][]string{"PAXGUSDT": {"XAUUSD"}, "XAUUSD": {"PAXGUSDT"}}}
	store := newFakeOppStore()
	e := New(qbus, []detector.Detector{d}, evaluator.Config{MarginRateAnnual: 4.5, DetectorFreshnessTTL: time.Minute}, store, fc, testCommissionResolver(t, nil, nil), time.Nanosecond, nil, 0, 0, 0, 0, 0)
	e.SetAssetEquiv(equiv)

	e.onQuote(context.Background(), bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
	if c := store.count(); c != 0 {
		t.Fatalf("stale XAU leg must produce nothing, got %d upserts", c)
	}
}
