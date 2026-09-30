// engine_publish_test.go —— D-004 P1#8：发布/落库去重（实质变化+TTL 心跳）。
package engine

import (
	"context"
	"testing"
	"time"

	"arb/internal/detector"
	"arb/internal/evaluator"
	"arb/internal/store/pgstore"
)

func pubTestEngine(store *fakeOppStore) *Engine {
	return &Engine{
		store:        store,
		evalCfg:      evaluator.Config{OpportunityTTL: 30 * time.Second},
		pubDeltaBps:  0.5,
		pubHeartbeat: 10 * time.Second,
		lastPub:      map[string]pubState{},
	}
}

// TestMaybePublish_SameNetDeduped 同 oppID 同 netBps 连续帧：upsert/publish 各 1 次。
func TestMaybePublish_SameNetDeduped(t *testing.T) {
	store := newFakeOppStore()
	e := pubTestEngine(store)
	ctx := context.Background()
	o := &pgstore.Opportunity{ID: "CARRY:AAA:va:vb", NetBps: 16}
	for i := 0; i < 3; i++ {
		e.maybePublish(ctx, o)
	}
	if c := store.callCount(); c != 1 {
		t.Fatalf("upsert calls=%d, want 1", c)
	}
}

// TestMaybePublish_DeltaTrigger netBps 变化 ≥pubDeltaBps → 发布；<阈值 → 不发布。
func TestMaybePublish_DeltaTrigger(t *testing.T) {
	store := newFakeOppStore()
	e := pubTestEngine(store)
	ctx := context.Background()
	o := &pgstore.Opportunity{ID: "o1", NetBps: 10}
	e.maybePublish(ctx, o)
	o.NetBps = 10.2 // Δ0.2 < 0.5 → 去重
	e.maybePublish(ctx, o)
	if c := store.callCount(); c != 1 {
		t.Fatalf("Δ0.2: upsert calls=%d, want 1", c)
	}
	o.NetBps = 10.8 // Δ0.6 ≥ 0.5（对 last 发布值 10）→ 发布
	e.maybePublish(ctx, o)
	if c := store.callCount(); c != 2 {
		t.Fatalf("Δ0.6: upsert calls=%d, want 2", c)
	}
}

// TestMaybePublish_Heartbeat 同值帧过心跳后再发布（TTL 续命）。
func TestMaybePublish_Heartbeat(t *testing.T) {
	store := newFakeOppStore()
	e := pubTestEngine(store)
	e.pubHeartbeat = 30 * time.Millisecond
	ctx := context.Background()
	o := &pgstore.Opportunity{ID: "o2", NetBps: 5}
	e.maybePublish(ctx, o)
	e.maybePublish(ctx, o)
	if c := store.callCount(); c != 1 {
		t.Fatalf("before heartbeat: upsert calls=%d, want 1", c)
	}
	time.Sleep(40 * time.Millisecond)
	e.maybePublish(ctx, o)
	if c := store.callCount(); c != 2 {
		t.Fatalf("after heartbeat: upsert calls=%d, want 2", c)
	}
}

// TestMaybePublish_PerOppID 不同 oppID 各自独立计数。
func TestMaybePublish_PerOppID(t *testing.T) {
	store := newFakeOppStore()
	e := pubTestEngine(store)
	ctx := context.Background()
	e.maybePublish(ctx, &pgstore.Opportunity{ID: "A", NetBps: 1})
	e.maybePublish(ctx, &pgstore.Opportunity{ID: "B", NetBps: 1})
	e.maybePublish(ctx, &pgstore.Opportunity{ID: "A", NetBps: 1}) // A 去重，B 不受影响
	if c := store.callCount(); c != 2 {
		t.Fatalf("upsert calls=%d, want 2（A 首见 + B 首见）", c)
	}
}

// TestFilteredCandidateNoLastPub 过滤失败（netStoreGate）的结果不进 lastPub。
func TestFilteredCandidateNoLastPub(t *testing.T) {
	cand := carryCand(20.0)
	e, _, _ := newNotifyEngine(t, cand)
	e.carryMinNetStore = 100.0 // net=16 < 100 → netStoreGate 拒
	scanCandidate(t, e, cand)
	e.mu.Lock()
	n := len(e.lastPub)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("lastPub=%d entries, want 0（过滤不进发布态）", n)
	}
}

// TestPrunePub 过期 lastPub 条目被清。
func TestPrunePub(t *testing.T) {
	store := newFakeOppStore()
	e := pubTestEngine(store)
	e.lastPub["stale"] = pubState{netBps: 1, at: time.Now().Add(-2 * time.Hour)}
	e.lastPub["fresh"] = pubState{netBps: 1, at: time.Now()}
	e.prunePub()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.lastPub["stale"]; ok {
		t.Error("stale entry not pruned")
	}
	if _, ok := e.lastPub["fresh"]; !ok {
		t.Error("fresh entry wrongly pruned")
	}
}

// fhCand 构造 FUNDING_HEDGE 候选（热修：cexPaperHook 发布节拍断言用）。
func fhCand(gross float64) detector.Candidate {
	c := carryCand(gross)
	c.Type = detector.TypeFundingHedge
	return c
}

// TestCexPaperHook_PublishPaced 热修 hotfix-loops（OOM 19:01 实证）：
// cexPaperHook 必须随发布去重节拍——同候选同值连续 5 帧 hook 仅 1 次；
// Δ≥pubDeltaBps 后第 2 次。帧率级调用=纸面开平+DB 写风暴回归。
func TestCexPaperHook_PublishPaced(t *testing.T) {
	cand := fhCand(20.0)
	e, _, _ := newNotifyEngine(t, cand)
	var calls int
	e.cexPaperHook = func(_ context.Context, cs []detector.Candidate) {
		calls += len(cs)
	}
	for i := 0; i < 5; i++ {
		scanCandidate(t, e, cand)
	}
	if calls != 1 {
		t.Fatalf("same-value 5 frames: hook calls=%d, want 1（发布去重节拍）", calls)
	}
	// Δ：gross 20→21 → netBps 变化 ≥pubDeltaBps(0.5) → 再发布 → hook 第 2 次。
	cand2 := fhCand(21.0)
	e.detectors[0].(*fakeDetector).candidates = []detector.Candidate{cand2}
	scanCandidate(t, e, cand2)
	if calls != 2 {
		t.Fatalf("delta frame: hook calls=%d, want 2", calls)
	}
}
