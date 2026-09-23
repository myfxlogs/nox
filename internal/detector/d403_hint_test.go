// d403_hint_test.go —— D-403 S6：detector universe-hint 配对路径。
// 同 canonical cfd×perp（BTC 型，asset_equiv 配对盲区）有报价→产 XASSET_CARRY 候选
// （同 canonical 断言 Canonical==CanonicalSell）；无报价→nil；net 门槛生效；nil hint 源向后兼容。
package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// fakeHints 测试用 CfdPairHintSource。
type fakeHints struct{ hints []CfdPairHint }

func (f fakeHints) CfdPairs(context.Context) []CfdPairHint { return f.hints }

// btcHint 同 canonical 型：BTCUSD@1279967:mt5 × BTCUSDT@gate:perp。
var btcHints = fakeHints{hints: []CfdPairHint{
	{MtCanonical: "BTCUSD", CexCanonical: "BTCUSDT", MtVenueKey: "1279967:mt5", CexVenueKey: "gate:perp"},
}}

func btcSnap(now time.Time) map[string]map[string]bus.Quote {
	return map[string]map[string]bus.Quote{
		"BTCUSD": {
			"1279967:mt5": {Symbol: "BTCUSD", Venue: "1279967", VenueType: "mt5", Bid: 60000.0, Ask: 60010.0, Time: now},
		},
		"BTCUSDT": {
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 60000.0, Ask: 60010.0, Time: now},
		},
	}
}

func newHintDetector() *XAssetCarryDetector {
	return &XAssetCarryDetector{
		EntryMinBps:     -10,
		MinNetAnnualPct: 1.0,
		TrackingMaxBps:  50,
		FreshnessTTL:    60 * time.Second,
		// AssetEquiv=nil：hint 路径独立于等价类（asset_equiv 零 crypto 的盲区回归验证点）
		CfdHints: btcHints,
	}
}

func TestD403_Hint_SameCanonical_Pairs(t *testing.T) {
	now := time.Now()
	// funding 8.0 挂 CEX 腿（卖腿收/买腿付双向都评——funding 缺任一向即该向不评估）
	fp := fakeFP{funding: map[string]map[string]float64{"BTCUSDT": {"gate": 8.0}}}
	cands := newHintDetector().Scan(context.Background(), btcSnap(now), fp)
	if len(cands) == 0 {
		t.Fatal("同 canonical cfd×perp 有报价+funding 应产候选（asset_equiv 盲区回归锚）")
	}
	for _, c := range cands {
		if c.Type != TypeXAssetCarry {
			t.Errorf("type want XASSET_CARRY, got %s", c.Type)
		}
		if c.AssetClass != "" {
			t.Errorf("hint 路径 AssetClass 应留空（同标的语义），got %s", c.AssetClass)
		}
		if c.Canonical != c.CanonicalSell && !(c.Canonical == "BTCUSD" && c.CanonicalSell == "BTCUSDT") && !(c.Canonical == "BTCUSDT" && c.CanonicalSell == "BTCUSD") {
			t.Errorf("canonical 对异常: %s→%s", c.Canonical, c.CanonicalSell)
		}
		legs := map[string]bool{c.VenueBuy: true, c.VenueSell: true}
		if !legs["1279967:mt5"] || !legs["gate:perp"] {
			t.Errorf("双腿 venue 异常: %s×%s", c.VenueBuy, c.VenueSell)
		}
		if c.Canonical == c.CanonicalSell && c.VenueBuy != "1279967:mt5" {
			// 同 canonical 情形：Canonical==CanonicalSell 均为 Mt 形或 Cex 形（tryPair bc==sc 无假设）
			t.Errorf("同 canonical 候选 venue 归属异常: %s", c.VenueBuy)
		}
	}
}

func TestD403_Hint_NoQuote_Nil(t *testing.T) {
	now := time.Now()
	snap := btcSnap(now)
	delete(snap["BTCUSDT"], "gate:perp") // CEX 腿无报价
	fp := fakeFP{funding: map[string]map[string]float64{"BTCUSDT": {"gate": 8.0}}}
	if cands := newHintDetector().Scan(context.Background(), snap, fp); len(cands) != 0 {
		t.Errorf("无报价应零候选，got %d", len(cands))
	}
}

func TestD403_Hint_NetGate(t *testing.T) {
	now := time.Now()
	// funding 负深：买 MT 卖 CEX 向 net=−8 < 1.0；买 CEX 卖 MT 向 funding 缺（gate 无 BTCUSD fact）
	// 也缺 BTCUSDT@gate？——funding 键=canonical→venue，双向都需各自 fact：给 CEX 腿 −20 →
	// 正向（卖 CEX 收 −20）net=−20 拒；反向（买 CEX 付 −20 → +20）但 swap 拖不垮 → 会产反向候选。
	// 门槛验证改为双向全负：funding=+20 时正向收 +20 产候选、反向付 −20 拒 → 断言恰 1 条。
	fp := fakeFP{funding: map[string]map[string]float64{"BTCUSDT": {"gate": -20.0}}}
	snap := btcSnap(now)
	// funding 只按卖/买腿计：正向买 MT 卖 CEX 收 funding(−20) → net −20 < 1 拒
	// 反向买 CEX 卖 MT 付 funding(−20→ −(−20)=+20) → net +20 产
	cands := newHintDetector().Scan(context.Background(), snap, fp)
	if len(cands) != 1 {
		t.Fatalf("funding −20：仅反向（买 CEX 卖 MT）过 net 门槛，want 1 got %d", len(cands))
	}
	if cands[0].VenueBuy != "gate:perp" {
		t.Errorf("反向候选买腿应=gate:perp，got %s", cands[0].VenueBuy)
	}
}

func TestD403_Hint_NilSource_BackwardCompat(t *testing.T) {
	now := time.Now()
	d := newHintDetector()
	d.CfdHints = nil // 既有构造点（无 hint 注入）零产出零 panic
	fp := fakeFP{}
	if cands := d.Scan(context.Background(), goldSnap(now), fp); len(cands) != 0 {
		t.Errorf("nil hint 源应零候选，got %d", len(cands))
	}
}
