package dashboard

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/reconcile"
)

// fakeHealthStore 健康监控 fake store（D-104 P0 单测）。
type fakeHealthStore struct {
	mu     sync.Mutex
	facts  map[string][]fact.Fact // kind → facts
	alerts []fakeAlert
}

type fakeAlert struct {
	level string
	msg   string
}

func newFakeHealthStore() *fakeHealthStore {
	return &fakeHealthStore{facts: make(map[string][]fact.Fact)}
}

func (s *fakeHealthStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.facts[kind], nil
}

func (s *fakeHealthStore) InsertAlert(_ context.Context, _ *int64, level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, fakeAlert{level: level, msg: message})
	return nil
}

func (s *fakeHealthStore) setFacts(kind string, fs []fact.Fact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts[kind] = fs
}

func (s *fakeHealthStore) alertCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}

func (s *fakeHealthStore) snapshot() []fakeAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]fakeAlert, len(s.alerts))
	copy(out, s.alerts)
	return out
}

// fakeVenueSource 健康监控 fake venue source。
type fakeVenueSource struct {
	queriers []reconcile.AccountQuerier
}

func (f *fakeVenueSource) AccountQueriers() []reconcile.AccountQuerier { return f.queriers }

// TestMTHealth_FirstTickSilent 首 tick 只记状态不告警（避免重启动噪音）。
func TestMTHealth_FirstTickSilent(t *testing.T) {
	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Errorf("first tick should not alert, got %d alerts", got)
	}
}

// TestMTHealth_StateTransitions healthy→warn→crit→recover 各转换恰一次告警/持续同态零重复。
func TestMTHealth_StateTransitions(t *testing.T) {
	// 抑制日志噪音
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := time.Now()

	// tick1: healthy（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick, healthy): want 0 alerts, got %d", got)
	}

	// tick2: 报价 6m 前 → warn（healthy→warn 转换，1 alert）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-6 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (warn transition): want 1 alert, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[0].level != "warn" {
		t.Errorf("tick2: want warn, got %s", alerts[0].level)
	}

	// tick3: 报价 31m 前 → crit（warn→crit 转换，1 alert）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-31 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Fatalf("tick3 (crit transition): want 2 alerts, got %d", got)
	}
	alerts = store.snapshot()
	if alerts[1].level != "critical" {
		t.Errorf("tick3: want critical, got %s", alerts[1].level)
	}

	// tick4: 报价仍 31m 前 → crit（持续同态，0 new alerts）
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Fatalf("tick4 (crit sustained): want 2 alerts (no new), got %d", got)
	}

	// tick5: 报价恢复 fresh → healthy（crit→healthy 转换，1 info alert）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 3 {
		t.Fatalf("tick5 (recover): want 3 alerts, got %d", got)
	}
	alerts = store.snapshot()
	if alerts[2].level != "info" {
		t.Errorf("tick5: want info recovered, got %s", alerts[2].level)
	}
}

// TestMTHealth_SwapFactStale swap facts 陈旧 → crit 告警（漏轮探测）。
func TestMTHealth_SwapFactStale(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := time.Now()
	// tick1: 报价 fresh + swap facts fresh（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now, Src: "mt5"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0 alerts, got %d", got)
	}

	// tick2: swap facts 7h 前 → crit（factWarn=2h, factCrit=6h, 7h>6h → crit）
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})
	mon.poll(context.Background())
	// quote healthy + swap crit + margin healthy → 1 alert (swap crit)
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (swap stale crit): want 1 alert, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[0].level != "critical" {
		t.Errorf("tick2: want critical, got %s", alerts[0].level)
	}
	if !strings.Contains(alerts[0].msg, "swap") {
		t.Errorf("tick2: want swap metric, got msg=%s", alerts[0].msg)
	}
}

// TestMTHealth_NoDataCrit 无数据 venue → crit（从未采集=异常）。
// 首 tick 有数据（healthy 静默），tick2 数据消失 → crit 转换告警。
func TestMTHealth_NoDataCrit(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := time.Now()
	// tick1: 有数据（首 tick 静默，记 healthy 状态）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now, Src: "mt5"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick, healthy): want 0 alerts, got %d", got)
	}

	// tick2: 数据消失（新 QuoteBus 无报价 + 清 facts）→ quote+swap+margin 全 crit
	qbus2 := bus.New() // 新 bus 无报价
	mon.qbus = qbus2
	store.setFacts(fact.KindSwapLong, nil)
	store.setFacts(fact.KindSwapShort, nil)
	store.setFacts(fact.KindInitMarginRate, nil)
	mon.poll(context.Background())
	if got := store.alertCount(); got != 3 {
		t.Fatalf("tick2 (no data crit): want 3 alerts (quote+swap+margin), got %d", got)
	}
	alerts := store.snapshot()
	for i, a := range alerts {
		if a.level != "critical" {
			t.Errorf("alert[%d]: want critical, got %s", i, a.level)
		}
	}
}
