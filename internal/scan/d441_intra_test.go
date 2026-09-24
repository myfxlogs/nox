// d441_intra_test.go —— D-441 T2：kill/circuit 事件落库+critical 告警；正常 stop 零事件零告警。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func d441OpenTrade() pgstore.IntraTrade {
	return pgstore.IntraTrade{
		ID: 1, Baseline: "momentum_break", Symbol: "XAUUSDm", Venue: "mt5",
		Side: "long", Status: "open", EntryTs: time.Now().Add(-time.Hour),
		EntryPx: 100, Qty: 0.01, NotionalUSD: 1, MarginUSD: 1, StopPx: 99,
	}
}

func d441Tick(bid, ask float64) []pgstore.TickRow {
	return []pgstore.TickRow{{Ts: time.Now(), Bid: bid, Ask: ask}}
}

// T2a：kill 路径 → 1×kill 事件 + 1×critical 告警。
func TestD441KillEmitsEventAndAlert(t *testing.T) {
	f := &fakeIntraStore{open: []pgstore.IntraTrade{d441OpenTrade()}, ticks: map[string][]pgstore.TickRow{}}
	eng := NewIntraEngine(f, d425Cfg(true))
	var alerts []string
	eng.SetAlertFn(func(_ context.Context, level, msg string) { alerts = append(alerts, level+":"+msg) })
	if _, err := eng.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(f.events) != 1 || f.events[0] != "kill" {
		t.Fatalf("events=%v want [kill]", f.events)
	}
	if len(alerts) != 1 || alerts[0][:9] != "critical:" {
		t.Fatalf("alerts=%v want 1 critical", alerts)
	}
}

// T2b：circuit 路径 → 1×circuit 事件 + 1×critical 告警。
func TestD441CircuitEmitsEventAndAlert(t *testing.T) {
	f := &fakeIntraStore{
		ticks:  map[string][]pgstore.TickRow{},
		dayPnL: -5000, // ≤ dayBudget=−10000×30%=−3000
	}
	eng := NewIntraEngine(f, d425Cfg(false))
	var alerts []string
	eng.SetAlertFn(func(_ context.Context, level, msg string) { alerts = append(alerts, level+":"+msg) })
	rep, err := eng.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if !rep.CircuitHit {
		t.Fatal("expect CircuitHit")
	}
	if len(f.events) != 1 || f.events[0] != "circuit" {
		t.Fatalf("events=%v want [circuit]", f.events)
	}
	if len(alerts) != 1 || alerts[0][:9] != "critical:" {
		t.Fatalf("alerts=%v want 1 critical", alerts)
	}
}

// T2c：正常 stop 平仓路径 → 零事件零告警（噪音纪律）。
func TestD441StopNoEventNoAlert(t *testing.T) {
	f := &fakeIntraStore{
		open:   []pgstore.IntraTrade{d441OpenTrade()},
		ticks:  map[string][]pgstore.TickRow{"mt5|XAUUSDm": d441Tick(98, 98)}, // −2% < −0.8% 止损带
		dayPnL: 0,
	}
	eng := NewIntraEngine(f, d425Cfg(false))
	var alerts []string
	eng.SetAlertFn(func(_ context.Context, level, msg string) { alerts = append(alerts, level+":"+msg) })
	rep, err := eng.Step(context.Background())
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if rep.StopClosed != 1 {
		t.Fatalf("StopClosed=%d want 1", rep.StopClosed)
	}
	if len(f.events) != 0 || len(alerts) != 0 {
		t.Fatalf("events=%v alerts=%v want both empty", f.events, alerts)
	}
}

// T2d：持续 kill/circuit 态下第二零事件零告警（episode 当日去重=噪音纪律）。
func TestD441EventDedupSameDay(t *testing.T) {
	f := &fakeIntraStore{open: []pgstore.IntraTrade{d441OpenTrade()}, ticks: map[string][]pgstore.TickRow{}}
	eng := NewIntraEngine(f, d425Cfg(true))
	var alerts []string
	eng.SetAlertFn(func(_ context.Context, level, msg string) { alerts = append(alerts, level+":"+msg) })
	for i := 0; i < 2; i++ {
		if _, err := eng.Step(context.Background()); err != nil {
			t.Fatalf("step#%d: %v", i, err)
		}
	}
	if len(f.events) != 1 || len(alerts) != 1 {
		t.Fatalf("events=%v alerts=%v want 1/1", f.events, alerts)
	}
}
