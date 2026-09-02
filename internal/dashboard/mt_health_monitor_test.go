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
	mu      sync.Mutex
	facts   map[string][]fact.Fact // kind → facts
	aliases map[string]string      // venue → alias（D-203）
	alerts  []fakeAlert
}

type fakeAlert struct {
	level string
	msg   string
}

func newFakeHealthStore() *fakeHealthStore {
	return &fakeHealthStore{facts: make(map[string][]fact.Fact), aliases: make(map[string]string)}
}

func (s *fakeHealthStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.facts[kind], nil
}

func (s *fakeHealthStore) ListAccountAliases(_ context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.aliases))
	for k, v := range s.aliases {
		out[k] = v
	}
	return out, nil
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

// wedUTC 固定非周末 UTC 时刻（D-136 修复②：mt-health 测试注入时钟，防周末静默窗致告警全静默）。
var wedUTC = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) // Wednesday

// TestMTHealth_FirstTickSilent 首 tick 只记状态不告警（避免重启动噪音）。
func TestMTHealth_FirstTickSilent(t *testing.T) {
	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)
	mon.now = func() time.Time { return wedUTC.Add(time.Second) } // D-136 修复②：注入非周末时钟防日期依赖
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Errorf("first tick should not alert, got %d alerts", got)
	}
}

// TestMTHealth_StateTransitions healthy→warn→crit→recover 各转换恰一次告警/持续同态零重复。
// 注：swap/margin 无数据=持续 crit，R-c3 后 tick2 首次 crit 告警（!ok → realert），之后窗口内不重发。
func TestMTHealth_StateTransitions(t *testing.T) {
	// 抑制日志噪音
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟

	// tick1: quote healthy（首 tick 静默）；swap/margin 无数据=crit（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick, healthy): want 0 alerts, got %d", got)
	}

	// tick2: 报价 6m 前 → warn（转换，1 alert）+ swap/margin 持续 crit 首次告警（R-c3 !ok → 2 alerts）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-6 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 3 {
		t.Fatalf("tick2 (warn transition + swap/margin crit realert): want 3 alerts, got %d", got)
	}
	alerts := store.snapshot()
	// 1 warn (quote) + 2 critical (swap/margin sustained crit realert)
	levels := map[string]int{}
	for _, a := range alerts {
		levels[a.level]++
	}
	if levels["warn"] != 1 {
		t.Errorf("tick2: want 1 warn, got %d", levels["warn"])
	}
	if levels["critical"] != 2 {
		t.Errorf("tick2: want 2 critical (swap+margin realert), got %d", levels["critical"])
	}

	// tick3: 报价 31m 前 → crit（转换，1 alert）；swap/margin 持续 crit 窗口内不重发
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-31 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 4 {
		t.Fatalf("tick3 (crit transition): want 4 alerts, got %d", got)
	}
	alerts = store.snapshot()
	if alerts[3].level != "critical" {
		t.Errorf("tick3: want critical, got %s", alerts[3].level)
	}

	// tick4: 报价仍 31m 前 → crit（持续同态，窗口内不重发）；swap/margin 同理
	mon.poll(context.Background())
	if got := store.alertCount(); got != 4 {
		t.Fatalf("tick4 (crit sustained): want 4 alerts (no new), got %d", got)
	}

	// tick5: 报价恢复 fresh → healthy（crit→healthy 转换，1 info alert）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 5 {
		t.Fatalf("tick5 (recover): want 5 alerts, got %d", got)
	}
	alerts = store.snapshot()
	if alerts[4].level != "info" {
		t.Errorf("tick5: want info recovered, got %s", alerts[4].level)
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

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
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

// TestMTHealth_CritRealert crit 持续 >30min 重发一条（R-c3 去重窗口）。
func TestMTHealth_CritRealert(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
	// tick1: 全 healthy（首 tick 静默）——报价+swap+margin 都 fresh
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now, Src: "mt5"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0, got %d", got)
	}

	// tick2: 报价 31m 前 → quote crit（转换，1 alert；swap/margin 仍 healthy）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-31 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (crit transition): want 1, got %d", got)
	}

	// tick3: 持续 crit，窗口内（<30min）→ 不重发
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick3 (crit sustained <30min): want 1 (no realert), got %d", got)
	}

	// tick4: 模拟窗口过期——手动回拨 lastCritAlert 到 31min 前
	key := "vA:quote"
	mon.mu.Lock()
	mon.lastCritAlert[key] = now.Add(-31 * time.Minute)
	mon.mu.Unlock()
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Fatalf("tick4 (crit sustained >30min): want 2 (realert), got %d", got)
	}
	alerts := store.snapshot()
	if alerts[1].level != "critical" {
		t.Errorf("tick4: want critical, got %s", alerts[1].level)
	}
}

// TestMTHealth_WarnSustainedNoRealert warn 持续同态不重发（R-c3 只对 crit 重告警）。
func TestMTHealth_WarnSustainedNoRealert(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
	// tick1: 全 healthy（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now, Src: "mt5"}})
	mon.poll(context.Background())

	// tick2: 报价 6m 前 → quote warn（转换，1 alert；swap/margin 仍 healthy）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now.Add(-6 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (warn transition): want 1, got %d", got)
	}

	// tick3: 持续 warn → 仍不重发（warn 不受 R-c3 影响）
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick3 (warn sustained): want 1 (no realert for warn), got %d", got)
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

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
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

// TestMTHealth_CEXVenueNoSwapMarginAlerts R-d2：CEX-like venue（有报价、无 AccountQuerier）
// → 不产生 swap/margin 告警（含 R-c3 重发路径）；quote 告警照常。
func TestMTHealth_CEXVenueNoSwapMarginAlerts(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	// CEX venue "binance" 有报价但无 AccountQuerier（不在 mtSet）
	vs := &fakeVenueSource{queriers: nil}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
	// tick1: fresh（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0, got %d", got)
	}

	// tick2: 报价 31m 前 → quote crit（转换，1 alert）；swap/margin 不适用 CEX → 0 alerts
	qbus.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", Bid: 100, Ask: 100, Time: now.Add(-31 * time.Minute)})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (quote crit only): want 1 alert, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[0].level != "critical" || !strings.Contains(alerts[0].msg, "quote") {
		t.Errorf("tick2: want quote critical, got %s: %s", alerts[0].level, alerts[0].msg)
	}

	// tick3: 持续 quote crit，回拨 lastCritAlert 31min → quote 重发 1 条；
	// swap/margin 即使 R-c3 重发路径也不触发（非 MT venue 不进 maybeAlert）
	mon.mu.Lock()
	mon.lastCritAlert["binance:quote"] = now.Add(-31 * time.Minute)
	mon.mu.Unlock()
	mon.poll(context.Background())
	if got := store.alertCount(); got != 2 {
		t.Fatalf("tick3 (quote realert): want 2, got %d", got)
	}
	// 确认无 swap/margin 告警
	for _, a := range store.snapshot() {
		if strings.Contains(a.msg, "swap") || strings.Contains(a.msg, "margin") {
			t.Errorf("CEX venue 不应有 swap/margin 告警，got %s", a.msg)
		}
	}
}

// TestMTHealth_MTVenueSwapCritRegress R-d2：MT venue 无 swap facts → swap crit 告警照旧（R-c3 回归，防误伤真信号）。
func TestMTHealth_MTVenueSwapCritRegress(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) } // D-136 修复②：注入非周末时钟
	// tick1: quote fresh + swap/margin fresh（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now, Src: "mt5"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0, got %d", got)
	}

	// tick2: swap facts 7h 前 → swap crit（转换，1 alert）；quote/margin healthy
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 1 {
		t.Fatalf("tick2 (MT swap crit): want 1 alert, got %d", got)
	}
	alerts := store.snapshot()
	if alerts[0].level != "critical" || !strings.Contains(alerts[0].msg, "swap") {
		t.Errorf("tick2: want MT swap critical, got %s: %s", alerts[0].level, alerts[0].msg)
	}
}

// TestMTHealth_MT4VenueNoMarginAlert D-203：MT4 venue 无 margin facts（采集器只挂 MT5），
// margin 永久 healthy 不告警。swap 仍正常检查（MT4 有 swap facts）。
// 场景：MT4 venue "vB:mt4" 无 init_margin_rate fact → tick2 margin 不告警；
// swap fact 7h 前 → swap crit 告警（验证 swap 仍对 MT4 生效）。
func TestMTHealth_MT4VenueNoMarginAlert(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vB:mt4"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) }

	// tick1: quote fresh + swap fresh（首 tick 静默）。无 margin fact（MT4 不采集）。
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", Bid: 100, Ask: 100, Time: now})
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vB", Symbol: "AUDJPY", Value: 5, Ts: now, Src: "mt4"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3, Ts: now, Src: "mt4"}})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0, got %d", got)
	}

	// tick2: swap 7h 前 → swap crit（1 alert）；margin 无 fact 但 MT4 不检查 → 0 margin alert
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vB", Symbol: "AUDJPY", Value: 5, Ts: now.Add(-7 * time.Hour), Src: "mt4"}})
	store.setFacts(fact.KindSwapShort, []fact.Fact{{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3, Ts: now.Add(-7 * time.Hour), Src: "mt4"}})
	mon.poll(context.Background())

	alerts := store.snapshot()
	// 只应有 1 条 swap critical，不应有 margin critical
	var swapCrit, marginCrit int
	for _, a := range alerts {
		if strings.Contains(a.msg, "margin") && a.level == "critical" {
			marginCrit++
		}
		if strings.Contains(a.msg, "swap") && a.level == "critical" {
			swapCrit++
		}
	}
	if marginCrit != 0 {
		t.Errorf("MT4 venue 不应发 margin critical（无 margin facts），got %d", marginCrit)
	}
	if swapCrit != 1 {
		t.Errorf("MT4 venue swap crit 应发 1 条，got %d", swapCrit)
	}
}

// TestMTHealth_MT5VenueMarginCritStillWorks D-203 回归：MT5 venue 无 margin fact → 仍发 critical（不误修）。
func TestMTHealth_MT5VenueMarginCritStillWorks(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vC:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) }

	// tick1: quote fresh，无 swap/margin fact（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vC", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (first tick): want 0, got %d", got)
	}

	// tick2: 无 margin fact → margin crit 告警（MT5 仍检查 margin）
	mon.poll(context.Background())

	var marginCrit int
	for _, a := range store.snapshot() {
		if strings.Contains(a.msg, "margin") && a.level == "critical" {
			marginCrit++
		}
	}
	if marginCrit != 1 {
		t.Errorf("MT5 venue 无 margin fact 应发 1 条 margin critical，got %d", marginCrit)
	}
}

// TestMTHealth_AlertUsesAlias D-203：告警消息用别名而非裸账号。
// 场景：venue "vD:mt5" alias "EXR-test" → margin critical 告警消息含 "EXR-test" 不含 "vD"。
func TestMTHealth_AlertUsesAlias(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	store.aliases["vD"] = "EXR-test"
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vD:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) }

	// tick1: quote fresh（首 tick 静默）
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vD", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())

	// tick2: 无 margin fact → margin crit 告警，消息应用别名 "EXR-test"
	mon.poll(context.Background())

	var foundAlias bool
	for _, a := range store.snapshot() {
		if strings.Contains(a.msg, "margin") && a.level == "critical" {
			if strings.Contains(a.msg, "EXR-test") && !strings.Contains(a.msg, "vD ") {
				foundAlias = true
			}
		}
	}
	if !foundAlias {
		t.Errorf("margin critical 告警应用别名 EXR-test，实际消息：%v", store.snapshot())
	}
}

// TestMTHealth_AliasFallback D-203：无别名时回退裸账号。
func TestMTHealth_AliasFallback(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore() // 无 aliases 设置
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vE:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) }

	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vE", Bid: 100, Ask: 100, Time: now})
	mon.poll(context.Background())
	mon.poll(context.Background())

	var foundVenue bool
	for _, a := range store.snapshot() {
		if strings.Contains(a.msg, "margin") && a.level == "critical" && strings.Contains(a.msg, "vE") {
			foundVenue = true
		}
	}
	if !foundVenue {
		t.Errorf("无别名时应回退裸账号 vE，实际消息：%v", store.snapshot())
	}
}
