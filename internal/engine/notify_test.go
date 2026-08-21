package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/detector"
	"arb/internal/evaluator"
	"arb/internal/store/pgstore"
)

// fakeOppStore 模拟 UpsertOpportunity 的 inserted 判定（D-050）：新 id → true，重复 id → false。
type fakeOppStore struct {
	mu      sync.Mutex
	upserts map[string]bool
}

func newFakeOppStore() *fakeOppStore {
	return &fakeOppStore{upserts: make(map[string]bool)}
}

func (f *fakeOppStore) UpsertOpportunity(_ context.Context, o *pgstore.Opportunity) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, seen := f.upserts[o.ID]; seen {
		return false, nil
	}
	f.upserts[o.ID] = true
	return true, nil
}

func (f *fakeOppStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserts)
}

// fakeDetector 固定返回候选，驱动 evaluate → upsert → 发布链路。
type fakeDetector struct {
	candidates []detector.Candidate
}

func (d *fakeDetector) Type() detector.OppType {
	if len(d.candidates) > 0 {
		return d.candidates[0].Type
	}
	return detector.TypeCarry
}

func (d *fakeDetector) Scan(_ context.Context, _ map[string]map[string]bus.Quote, _ detector.FactProvider) []detector.Candidate {
	return d.candidates
}

// carryCand 构造 CARRY 候选（评估净收益 = gross − margin，测试 margin=4.0）。
func carryCand(gross float64) detector.Candidate {
	return detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "AAA",
		VenueBuy:  "va:spot",
		VenueSell: "vb:spot",
		PriceBuy:  100.0,
		PriceSell: 100.5,
		GrossBps:  gross,
		QuoteTime: time.Now().UnixMilli(),
	}
}

// newNotifyEngine 装配带 oppTopic 的引擎 + 订阅收集器。throttle 取极小值使同 symbol 连续扫描可过。
func newNotifyEngine(t *testing.T, cand detector.Candidate, minNotify float64) (*Engine, <-chan pgstore.Opportunity, *fakeOppStore) {
	t.Helper()
	qbus := bus.New()
	store := newFakeOppStore()
	cfg := evaluator.Config{
		MarginRateAnnual:     4.0,
		SlippageBps:          1.0,
		DetectorFreshnessTTL: time.Minute,
		OpportunityTTL:       30 * time.Second,
	}
	topic := bus.NewTopic[pgstore.Opportunity]()
	e := New(qbus, []detector.Detector{&fakeDetector{candidates: []detector.Candidate{cand}}}, cfg, store, NewFactCache(), testCommissionResolver(t, nil, nil), time.Nanosecond, topic, minNotify, 0)
	ch, cancel := topic.Subscribe()
	t.Cleanup(cancel)
	return e, ch, store
}

// scanCandidate 发布双腿报价并驱动一次扫描（onQuote）。
func scanCandidate(t *testing.T, e *Engine, cand detector.Candidate) {
	t.Helper()
	now := time.Now()
	qbuy := bus.Quote{Symbol: cand.Canonical, Venue: "va", VenueType: "spot", Bid: 100.0, Ask: 100.1, Time: now}
	qsell := bus.Quote{Symbol: cand.Canonical, Venue: "vb", VenueType: "spot", Bid: 100.5, Ask: 100.6, Time: now}
	e.bus.Publish(qbuy)
	e.bus.Publish(qsell)
	e.onQuote(context.Background(), qbuy)
}

// drainTopic 非阻塞收集已发布消息数。
func drainTopic(ch <-chan pgstore.Opportunity) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// TestNotifyNewOpportunity_NewInsertPublishes 新插入 net≥门槛 → 发布 1 条。
func TestNotifyNewOpportunity_NewInsertPublishes(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(20.0), 10.0) // net 16 ≥ 10
	scanCandidate(t, e, carryCand(20.0))
	if n := drainTopic(ch); n != 1 {
		t.Errorf("publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("upsert count want 1, got %d", c)
	}
}

// TestNotifyNewOpportunity_RefreshNotPublishes 同 id 刷新 → 不发布（仅新插入才推，30s 快照兜底）。
func TestNotifyNewOpportunity_RefreshNotPublishes(t *testing.T) {
	cand := carryCand(20.0)
	e, ch, store := newNotifyEngine(t, cand, 10.0)
	scanCandidate(t, e, cand) // 首次：inserted=true → 发布 1
	if n := drainTopic(ch); n != 1 {
		t.Fatalf("first scan publish count want 1, got %d", n)
	}
	scanCandidate(t, e, cand) // 同 id 刷新：fake 返回 inserted=false → 不发布
	if n := drainTopic(ch); n != 0 {
		t.Errorf("refresh publish count want 0, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("upsert count want 1（同 id 不新增行）, got %d", c)
	}
}

// TestNotifyNewOpportunity_BelowThresholdNotPublishes 新插入但 net<门槛 → 不发布（仍写库）。
func TestNotifyNewOpportunity_BelowThresholdNotPublishes(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(5.0), 10.0) // net 1 < 10
	scanCandidate(t, e, carryCand(5.0))
	if n := drainTopic(ch); n != 0 {
		t.Errorf("publish count want 0, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("upsert count want 1（低净值仍写库，仅不推送）, got %d", c)
	}
}

// TestNotifyNewOpportunity_TrapSkipsStoreAndPublish trap（net≤0）→ 不写库不发布。
func TestNotifyNewOpportunity_TrapSkipsStoreAndPublish(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(3.0), 10.0) // net -1 ≤ 0 → RatingTrap
	scanCandidate(t, e, carryCand(3.0))
	if n := drainTopic(ch); n != 0 {
		t.Errorf("publish count want 0, got %d", n)
	}
	if c := store.count(); c != 0 {
		t.Errorf("upsert count want 0（trap 不写库）, got %d", c)
	}
}
