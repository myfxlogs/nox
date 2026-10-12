// d067_margin_threshold_test.go —— D-068 锚：D-067 margin 独立阈值（warn 24h/crit 72h）
// 与 swap 共用阈值（2h/6h）同龄分叉——复刻 10-05 误报场景：同龄 7h 下 swap 该 crit、
// margin 不该。margin crit 走 D-251 闪断口径：raw crit 连续 ≥3 tick 才放行。
package dashboard

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/fact"
	"arb/internal/ledger/reconcile"
)

// mtStateFor 读 venue 当前三态（锁与 poll 对齐）。
func mtStateFor(mon *MTHealthMonitor, venue string) mtHealthState {
	mon.mu.Lock()
	defer mon.mu.Unlock()
	return mon.states[venue]
}

// countSwapCrit 统计 swap critical 告警数（同 countMarginCrit 形态）。
func countSwapCrit(store *fakeHealthStore) int {
	var n int
	for _, a := range store.snapshot() {
		if strings.Contains(a.msg, "隔夜息") && a.level == "critical" {
			n++
		}
	}
	return n
}

// TestD067_MarginIndependentThreshold margin 独立阈值锚：
// 同龄 7h → swap crit / margin healthy；25h → margin warn；73h → margin crit。
func TestD067_MarginIndependentThreshold(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	store := newFakeHealthStore()
	qbus := bus.New()
	vs := &fakeVenueSource{queriers: []reconcile.AccountQuerier{&fakeAccountQuerier{key: "vA:mt5"}}}
	mon := NewMTHealthMonitor(vs, qbus, store, time.Hour,
		5*time.Minute, 30*time.Minute, 2*time.Hour, 6*time.Hour, 24*time.Hour, 72*time.Hour)

	now := wedUTC
	mon.now = func() time.Time { return now.Add(time.Second) }
	mon.bootGraceTicks = 1 // 测试用 1 tick 宽限（生产默认 3）

	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", Bid: 100, Ask: 100, Time: now})
	// 10-05 误报场景：swap/margin 同龄 7h——swap 7h>6h 该 crit，margin 7h<24h 不该。
	store.setFacts(fact.KindSwapLong, []fact.Fact{{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now.Add(-7 * time.Hour), Src: "mt5"}})

	// tick1 宽限：只记状态不告警
	mon.poll(context.Background())
	if got := store.alertCount(); got != 0 {
		t.Fatalf("tick1 (boot grace): want 0 alerts, got %d", got)
	}

	// tick2: swap crit 恰 1 条；margin healthy 零 crit
	mon.poll(context.Background())
	if got := countSwapCrit(store); got != 1 {
		t.Fatalf("tick2: want 1 swap crit, got %d", got)
	}
	if got := countMarginCrit(store); got != 0 {
		t.Fatalf("tick2: margin 7h 同龄应零 crit, got %d", got)
	}
	if got := mtStateFor(mon, "vA").margin; got != metricHealthy {
		t.Errorf("tick2: want margin healthy, got %v", got)
	}

	// tick3: margin 25h → warn（24h≤age<72h），margin crit 仍 0
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now.Add(-25 * time.Hour), Src: "mt5"}})
	mon.poll(context.Background())
	if got := mtStateFor(mon, "vA").margin; got != metricWarn {
		t.Errorf("tick3: want margin warn, got %v", got)
	}
	if got := countMarginCrit(store); got != 0 {
		t.Fatalf("tick3: margin 25h 应 warn 零 crit, got %d", got)
	}

	// margin 73h → raw crit，D-251 闪断口径连续计数：count=1,2 抑制、count=3 放行
	store.setFacts(fact.KindInitMarginRate, []fact.Fact{{Kind: fact.KindInitMarginRate, Venue: "vA", Symbol: "vA", Value: 0.01, Ts: now.Add(-73 * time.Hour), Src: "mt5"}})
	mon.poll(context.Background()) // tick4: count=1 抑制
	mon.poll(context.Background()) // tick5: count=2 抑制
	if got := countMarginCrit(store); got != 0 {
		t.Fatalf("tick4-5: margin crit 连续计数 <3 应抑制, got %d", got)
	}
	mon.poll(context.Background()) // tick6: count=3 → crit 告警
	if got := countMarginCrit(store); got != 1 {
		t.Fatalf("tick6: want 1 margin crit (count=3), got %d", got)
	}
	if got := mtStateFor(mon, "vA").margin; got != metricCrit {
		t.Errorf("tick6: want margin crit state, got %v", got)
	}
}
