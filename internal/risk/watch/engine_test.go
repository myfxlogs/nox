// engine_test.go —— 盯盘引擎锚：tick 触发/once 自翻/cooldown 防抖/stale 不触发/venue_key 过滤。
package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

type fakeStore struct {
	mu      sync.Mutex
	rules   []pgstore.WatchRule
	alerts  []string
	fired   map[int64]bool // disable 标记集
	fireCnt map[int64]int
}

func (f *fakeStore) ListWatchRules(context.Context) ([]pgstore.WatchRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pgstore.WatchRule(nil), f.rules...), nil
}

func (f *fakeStore) MarkWatchFired(_ context.Context, id int64, disable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fireCnt[id]++
	if disable {
		f.fired[id] = true
	}
	for i := range f.rules {
		if f.rules[i].ID == id {
			t := time.Now()
			f.rules[i].LastFiredAt = &t
			if disable {
				f.rules[i].Enabled = false
			}
		}
	}
	return nil
}

func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, level+":"+msg)
	return nil
}

func (f *fakeStore) cnt(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fireCnt[id]
}

func (f *fakeStore) alertN() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.alerts)
}

func (f *fakeStore) setVenueKey(id int64, vk string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.rules {
		if f.rules[i].ID == id {
			f.rules[i].VenueKey = vk
		}
	}
}

func newFake() *fakeStore {
	return &fakeStore{fired: map[int64]bool{}, fireCnt: map[int64]int{}}
}

func runEngine(t *testing.T, e *Engine, qbus *bus.QuoteBus) (context.CancelFunc, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx, nil); close(done) }()
	e.Reload(ctx)
	time.Sleep(30 * time.Millisecond) // 等 Run 内通配订阅就位（否则 publish 早于订阅丢帧）
	return cancel, done
}

func TestWatchEngine_FireOnce(t *testing.T) {
	qbus := bus.New()
	fs := newFake()
	fs.rules = []pgstore.WatchRule{
		{ID: 1, Kind: "price", Subject: "TRXUSDT", Op: "ge", Threshold: 0.3, FireMode: "once", Enabled: true},
	}
	e := New(fs, qbus, time.Minute)
	cancel, _ := runEngine(t, e, qbus)
	defer cancel()

	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.34, Ask: 0.34, Time: time.Now()})
	deadline := time.After(2 * time.Second)
	for fs.cnt(1) == 0 {
		select {
		case <-deadline:
			t.Fatal("未触发")
		case <-time.After(5 * time.Millisecond):
		}
		fs.mu.Lock()
		fs.mu.Unlock()
	}
	if fs.alertN() != 1 {
		t.Fatalf("alerts=%d", fs.alertN())
	}
	// once 自翻 disabled → 后续 tick 不再触发
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.35, Ask: 0.35, Time: time.Now()})
	time.Sleep(30 * time.Millisecond)
	fs.mu.Lock()
	fired := fs.fired[1]
	fs.mu.Unlock()
	if fs.cnt(1) != 1 || !fired {
		t.Fatalf("fireCnt=%d fired=%v（once 应自翻退役）", fs.cnt(1), fired)
	}
}

func TestWatchEngine_NoFire(t *testing.T) {
	qbus := bus.New()
	fs := newFake()
	fs.rules = []pgstore.WatchRule{
		{ID: 2, Kind: "price", Subject: "BTCUSDT", Op: "gt", Threshold: 100000, FireMode: "once", Enabled: true},
		{ID: 3, Kind: "price", Subject: "TRXUSDT", Op: "ge", Threshold: 0.3, VenueKey: "okx:perp", FireMode: "once", Enabled: true},
	}
	e := New(fs, qbus, time.Minute)
	cancel, _ := runEngine(t, e, qbus)
	defer cancel()

	// 未达阈值 + venue_key 不匹配 → 零触发
	qbus.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "perp", Bid: 90000, Ask: 90000, Time: time.Now()})
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.4, Ask: 0.4, Time: time.Now()})
	time.Sleep(30 * time.Millisecond)
	if fs.alertN() != 0 {
		t.Fatalf("误触发 alerts=%d", fs.alertN())
	}
	// 陈旧 tick 不触发
	fs.setVenueKey(3, "")
	e.Reload(context.Background())
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "okx", VenueType: "perp", Bid: 0.4, Ask: 0.4, Time: time.Now().Add(-10 * time.Minute)})
	time.Sleep(30 * time.Millisecond)
	if fs.alertN() != 0 {
		t.Fatalf("陈旧 tick 误触发 alerts=%d", fs.alertN())
	}
}

func TestWatchEngine_RepeatCooldown(t *testing.T) {
	qbus := bus.New()
	fs := newFake()
	fs.rules = []pgstore.WatchRule{
		{ID: 4, Kind: "price", Subject: "TRXUSDT", Op: "ge", Threshold: 0.3, FireMode: "repeat", CooldownSec: 3600, Enabled: true},
	}
	e := New(fs, qbus, time.Minute)
	cancel, _ := runEngine(t, e, qbus)
	defer cancel()

	pub := func() {
		qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.4, Ask: 0.4, Time: time.Now()})
	}
	pub()
	time.Sleep(30 * time.Millisecond)
	pub() // cooldown 内二发 → 防抖
	time.Sleep(30 * time.Millisecond)
	if fs.cnt(4) != 1 {
		t.Fatalf("repeat cooldown 内 fireCnt=%d, want 1", fs.cnt(4))
	}
}
