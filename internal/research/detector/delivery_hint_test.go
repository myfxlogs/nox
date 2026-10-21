// delivery_hint_test.go —— D-649续③ armed 交割配对锚测：basis 年化入 net/
// tracking 豁免/tenor 缺 fail-closed/白名单闸沿用。
package detector

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
)

type fakeDelivHints struct{ pairs []DeliveryPairHint }

func (f *fakeDelivHints) DeliveryPairs(context.Context) []DeliveryPairHint { return f.pairs }

func delivQuote(sym, vk string, bid, ask float64) bus.Quote {
	return bus.Quote{VenueSymbol: sym, Venue: "okx", VenueType: keydomain.TypeOf(vk), Bid: bid, Ask: ask, Time: time.Now()}
}

func TestDeliveryHint_BasisAnnIntoNet(t *testing.T) {
	// spot 4000 vs futures 4200：premium=5%；tenor=30d → basis_ann≈60.8%>min。
	d := &XAssetCarryDetector{
		MinNetAnnualPct: 1, TrackingMaxBps: 50, FreshnessTTL: time.Minute,
		DeliveryHints: &fakeDelivHints{pairs: []DeliveryPairHint{{
			BuyCanonical: "ETHUSD", SellCanonical: "ETH-USD_UM-261225",
			BuyVenueKey: "okx:spot", SellVenueKey: "okx:futures", SellTenorDays: 30,
		}}},
	}
	snap := map[string]map[string]bus.Quote{
		"ETHUSD":            {"okx:spot": delivQuote("ETH-USDT", "okx:spot", 3999, 4000)},
		"ETH-USD_UM-261225": {"okx:futures": delivQuote("ETH-USD-261225", "okx:futures", 4200, 4201)},
	}
	out := d.Scan(context.Background(), snap, nil)
	if len(out) != 1 {
		t.Fatalf("候选 %d≠1", len(out))
	}
	// net≈(4200−4000)/4000×365/30×100≈60.83
	if out[0].GrossBps < 55 || out[0].GrossBps > 66 {
		t.Fatalf("basis 年化净息错: %.2f", out[0].GrossBps)
	}
	if out[0].VenueBuy != "okx:spot" || out[0].VenueSell != "okx:futures" {
		t.Fatalf("venue 对错: %+v", out[0])
	}
}

// tracking 豁免：同帧 spot×futures 价差 5%>TrackingMaxBps=50——若不豁免本候选被拒。
// （上锚已隐式证豁免；此处显式钉：equivalent cfd 对同价差必拒）
func TestDeliveryHint_TrackingBypass(t *testing.T) {
	d := &XAssetCarryDetector{
		MinNetAnnualPct: -1e9, TrackingMaxBps: 50, FreshnessTTL: time.Minute,
		DeliveryHints: &fakeDelivHints{pairs: []DeliveryPairHint{{
			BuyCanonical: "ETHUSD", SellCanonical: "ETH-USD_UM-261225",
			BuyVenueKey: "okx:spot", SellVenueKey: "okx:futures", SellTenorDays: 30,
		}}},
	}
	snap := map[string]map[string]bus.Quote{
		"ETHUSD":            {"okx:spot": delivQuote("ETH-USDT", "okx:spot", 3999, 4000)},
		"ETH-USD_UM-261225": {"okx:futures": delivQuote("ETH-USD-261225", "okx:futures", 4200, 4201)},
	}
	if out := d.Scan(context.Background(), snap, nil); len(out) != 1 {
		t.Fatalf("delivery 对 tracking 豁免失效: %d", len(out))
	}
}

// tenor 缺（universe expiry 未联到）→ fail-closed 零候选（年化分母不臆）。
func TestDeliveryHint_TenorMissingFailClosed(t *testing.T) {
	d := &XAssetCarryDetector{
		MinNetAnnualPct: -1e9, FreshnessTTL: time.Minute,
		DeliveryHints: &fakeDelivHints{pairs: []DeliveryPairHint{{
			BuyCanonical: "ETHUSD", SellCanonical: "ETH-USD_UM-261225",
			BuyVenueKey: "okx:spot", SellVenueKey: "okx:futures", SellTenorDays: 0,
		}}},
	}
	snap := map[string]map[string]bus.Quote{
		"ETHUSD":            {"okx:spot": delivQuote("ETH-USDT", "okx:spot", 3999, 4000)},
		"ETH-USD_UM-261225": {"okx:futures": delivQuote("ETH-USD-261225", "okx:futures", 4200, 4201)},
	}
	if out := d.Scan(context.Background(), snap, nil); len(out) != 0 {
		t.Fatalf("tenor 缺仍产候选 %d", len(out))
	}
}

// Allow 闸沿用：白名单外 armed 配对不发射（发现域收口同款）。
func TestDeliveryHint_AllowGate(t *testing.T) {
	d := &XAssetCarryDetector{
		MinNetAnnualPct: -1e9, FreshnessTTL: time.Minute,
		Allow: func(_, _, _, _ string) bool { return false },
		DeliveryHints: &fakeDelivHints{pairs: []DeliveryPairHint{{
			BuyCanonical: "ETHUSD", SellCanonical: "ETH-USD_UM-261225",
			BuyVenueKey: "okx:spot", SellVenueKey: "okx:futures", SellTenorDays: 30,
		}}},
	}
	snap := map[string]map[string]bus.Quote{
		"ETHUSD":            {"okx:spot": delivQuote("ETH-USDT", "okx:spot", 3999, 4000)},
		"ETH-USD_UM-261225": {"okx:futures": delivQuote("ETH-USD-261225", "okx:futures", 4200, 4201)},
	}
	if out := d.Scan(context.Background(), snap, nil); len(out) != 0 {
		t.Fatalf("白名单外仍产候选 %d", len(out))
	}
}

// TestDeliveryHint_CanonKeyResolve 退回②伤②锚：hint canonical=品种归一名
// 'ETH'（combo#25 实形），spot 快照键=行情 canonical 'ETHUSDT'——原名键 miss
// 回退 +USDT 拼装命中；futures 协定键（delivCanon 同 hint 名）原名直中。
func TestDeliveryHint_CanonKeyResolve(t *testing.T) {
	d := &XAssetCarryDetector{
		MinNetAnnualPct: 1, TrackingMaxBps: 50, FreshnessTTL: time.Minute,
		DeliveryHints: &fakeDelivHints{pairs: []DeliveryPairHint{{
			BuyCanonical: "ETH", SellCanonical: "ETH",
			BuyVenueKey: "okx:spot", SellVenueKey: "okx:futures", SellTenorDays: 30,
		}}},
	}
	snap := map[string]map[string]bus.Quote{
		"ETHUSDT": {"okx:spot": delivQuote("ETH-USDT", "okx:spot", 3999, 4000)},
		"ETH":     {"okx:futures": delivQuote("ETH-USD_UM-261225", "okx:futures", 4200, 4201)},
	}
	out := d.Scan(context.Background(), snap, nil)
	if len(out) != 1 {
		t.Fatalf("canonical 键解析应中——候选 %d≠1", len(out))
	}
}
