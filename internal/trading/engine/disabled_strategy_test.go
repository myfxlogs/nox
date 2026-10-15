package engine

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/research/detector"
)

// TestDisabledStrategies_SuppressesOpportunity 验证 §1 自动淘汰闭环：
// strategy_stats.disabled=true 的策略产出 → 不入库不发布（系统已自证亏损，展示假机会误导）。
func TestDisabledStrategies_SuppressesOpportunity(t *testing.T) {
	cand := carryCand(20.0) // CARRY, net 16
	e, ch, store := newNotifyEngine(t, cand)
	e.SetDisabledStrategies(map[string]bool{"CARRY": true})
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 0 {
		t.Errorf("disabled strategy publish count want 0, got %d", n)
	}
	if c := store.count(); c != 0 {
		t.Errorf("disabled strategy upsert count want 0, got %d", c)
	}
}

// TestDisabledStrategies_NonDisabledPasses 未 disabled 的策略 → 正常入库发布。
func TestDisabledStrategies_NonDisabledPasses(t *testing.T) {
	cand := carryCand(20.0) // CARRY, net 16
	e, ch, store := newNotifyEngine(t, cand)
	e.SetDisabledStrategies(map[string]bool{"PERP_FUNDING": true, "CROSS_VENUE": true}) // CARRY 未禁用
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 1 {
		t.Errorf("non-disabled publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("non-disabled upsert count want 1, got %d", c)
	}
}

// TestDisabledStrategies_EmptySetPasses 空/nil disabled 集合 → 全部通过（测试默认行为不变）。
func TestDisabledStrategies_EmptySetPasses(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(20.0))
	// 不调用 SetDisabledStrategies（nil）→ 不排除。
	scanCandidate(t, e, carryCand(20.0))
	if n := drainTopic(ch); n != 1 {
		t.Errorf("nil disabled set publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("nil disabled set upsert count want 1, got %d", c)
	}
}

// TestDisabledStrategies_RuntimeUpdate 验证运行时刷新：先放行 → SetDisabledStrategies 禁用 → 再扫描被拦。
func TestDisabledStrategies_RuntimeUpdate(t *testing.T) {
	cand := carryCand(20.0)
	e, ch, store := newNotifyEngine(t, cand)
	// 第一次：无 disabled → 入库发布
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 1 {
		t.Fatalf("first scan publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Fatalf("first scan upsert count want 1, got %d", c)
	}
	// 运行时禁用 CARRY
	e.SetDisabledStrategies(map[string]bool{"CARRY": true})
	// 第二次：同 id 刷新但 CARRY 已 disabled → 不入库不发布
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 0 {
		t.Errorf("after disable publish count want 0, got %d", n)
	}
	if c := store.count(); c != 1 { // 仍 1（不新增，旧记录 TTL 自然过期）
		t.Errorf("after disable upsert count want 1（no new）, got %d", c)
	}
}

// TestTrialComboExempt_DisabledXAsset D-550：宿主策略退役但组合行=S2 桥试单
// （store.trial 键集命中）→ 候选照常入库发布；非试单组合/未标记行仍被掐流。
func TestTrialComboExempt_DisabledXAsset(t *testing.T) {
	now := time.Now()
	cand := detector.Candidate{
		Type:      detector.TypeXAssetCarry,
		Canonical: "PAXGUSDT", CanonicalSell: "XAUUSD", // 反向：买 CEX 卖 MT
		VenueBuy: "binance:perp", VenueSell: "15032313:mt5",
		PriceBuy: 4000.5, PriceSell: 4000.0, GrossBps: 80.0,
		QuoteTime: now.UnixMilli(),
	}
	e, ch, store := newNotifyEngine(t, cand)
	e.SetDisabledStrategies(map[string]bool{"XASSET_CARRY": true})
	e.SetAssetEquiv(&fakeEquivView{partners: map[string][]string{"PAXGUSDT": {"XAUUSD"}}})
	scan := func() {
		e.bus.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
		e.bus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "15032313", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now})
		e.onQuote(context.Background(), bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
	}
	// 未标试单 → 掐流不变。
	scan()
	if n := drainTopic(ch); n != 0 || store.count() != 0 {
		t.Fatalf("非试单组合应掐流: pub=%d upsert=%d", n, store.count())
	}
	// 标试单行（四元组精确键）→ 豁免入库。
	store.trial["PAXGUSDT|XAUUSD|binance:perp|15032313:mt5"] = true
	scan()
	if n := drainTopic(ch); n != 1 || store.count() != 1 {
		t.Fatalf("试单行应豁免: pub=%d upsert=%d", n, store.count())
	}
	// 四元组错配（venue 串位）不豁免——标记只认桥写行。
	store2 := newFakeOppStore()
	store2.trial["PAXGUSDT|XAUUSD|binance:perp|WRONG:mt5"] = true
	e2, ch2, _ := newNotifyEngine(t, cand)
	e2.store = store2
	e2.SetDisabledStrategies(map[string]bool{"XASSET_CARRY": true})
	e2.SetAssetEquiv(&fakeEquivView{partners: map[string][]string{"PAXGUSDT": {"XAUUSD"}}})
	_ = store2
	scan2 := func() {
		e2.bus.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
		e2.bus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "15032313", VenueType: "mt5", Bid: 3999.8, Ask: 4000.0, Time: now})
		e2.onQuote(context.Background(), bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 4000.0, Ask: 4000.5, Time: now})
	}
	scan2()
	if n := drainTopic(ch2); n != 0 || store2.count() != 0 {
		t.Fatalf("四元组错配不应豁免: pub=%d upsert=%d", n, store2.count())
	}
}
