// d051_prealow_test.go —— D-051①锚：cexPaperHook 消费 detector Allow 前候选流——
// 白名单拒绝+净阈下潜的 FUNDING_HEDGE 候选纸面可见，同时执行链（发布/落库）零产出。
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

// TestD051_PaperHookPreAllow Allow 全拒+funding=12（< detector 有效阈 14.95）：
// 执行链零 opp（ScanAll 后闸全拦），cexPaperHook 收到该候选（证据面独立）。
func TestD051_PaperHookPreAllow(t *testing.T) {
	qbus := bus.New()
	store := newFakeOppStore()
	cfg := evaluator.Config{
		MarginRateAnnual:     4.0,
		SlippageBps:          1.0,
		DetectorFreshnessTTL: time.Minute,
		OpportunityTTL:       30 * time.Second,
	}
	topic := bus.NewTopic[pgstore.Opportunity]()
	fh := &detector.FundingHedgeDetector{
		MinNetAnnualized: 4.0, BaselinePct: 10.95, FreshnessTTL: time.Minute,
		Allow: func(_, _, _, _ string) bool { return false },
	}
	fc := NewFactCache()
	fc.UpdateFacts([]fact.Fact{{Kind: fact.KindFunding, Symbol: "AAAUSDT", Venue: "okx", Value: 12.0}})
	e := New(qbus, []detector.Detector{fh}, cfg, store, fc, testCommissionResolver(t, nil, nil), 0.5, topic, 0, 0, 0, 0, 0)
	ch, cancel := topic.Subscribe()
	t.Cleanup(cancel)
	var got []detector.Candidate
	e.cexPaperHook = func(_ context.Context, cs []detector.Candidate) { got = append(got, cs...) }

	now := time.Now()
	qSpot := bus.Quote{Symbol: "AAAUSDT", Venue: "okx", VenueType: "spot", Bid: 100, Ask: 100.1, Time: now}
	qPerp := bus.Quote{Symbol: "AAAUSDT", Venue: "okx", VenueType: "perp", Bid: 100, Ask: 100.1, Time: now}
	e.bus.Publish(qSpot)
	e.bus.Publish(qPerp)
	e.onQuote(context.Background(), qSpot)

	if len(got) != 1 || got[0].Canonical != "AAAUSDT" {
		t.Fatalf("paper hook 应收 Allow 前候选 AAAUSDT，got %v", got)
	}
	if c := store.count(); c != 0 {
		t.Fatalf("执行链应零 opp 落库（Allow 全拒），got %d", c)
	}
	if n := drainTopic(ch); n != 0 {
		t.Fatalf("执行链应零发布，got %d", n)
	}
}
