package dashboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	"arb/internal/exec"
	"arb/internal/venue"

	v1 "arb/internal/gen/dashboardv1"
)

// --- D-571 包A 验收锚点（红绿锚）：逐仓事件化 ---

// countingQuerier 包装 fakeVenueQuerier 记查询次数（kick→回读断言用）。
type countingQuerier struct {
	*fakeVenueQuerier
	mu    sync.Mutex
	calls int
}

func (c *countingQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.fakeVenueQuerier.QueryOpenedOrders(ctx)
}
func (c *countingQuerier) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// newEvMonTest 组装事件化 monitor：返回 monitor + 事件订阅 chan + qbus（报价注入用）。
func newEvMonTest(t *testing.T, q venue.Querier) (*BrokerPositionMonitor, <-chan *v1.BrokerPositionEvent, *bus.QuoteBus) {
	t.Helper()
	qbus := bus.New()
	bm := NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier(q.VenueKey(), q)
	topic := bus.NewTopic[*v1.BrokerPositionEvent]()
	ch, _ := topic.Subscribe()
	m := NewBrokerPositionMonitor(bm, qbus, time.Minute, topic,
		NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) { return 0, errors.New("n/a") }),
		exchange.NewFundingIntervalCache(), nil)
	return m, ch, qbus
}

// drainEvents 取走事件 chan 缓冲（topic 丢旧保新=容量1，逐条收）。
func drainEvents(ch <-chan *v1.BrokerPositionEvent) []*v1.BrokerPositionEvent {
	var out []*v1.BrokerPositionEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// T1：REST 权威缺仓 → remove 事件（帧外仓不删，仅 REST 裁决）。
func TestD571_RestRemoveEmitsRemove(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)

	q.orders = nil
	m.Refresh(ctx)
	evs := drainEvents(ch)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	rm := evs[0].GetRemove()
	if rm == nil || rm.VenueKey != "v:mt5" || rm.Ticket != "1" {
		t.Fatalf("want remove v:mt5|1, got %+v", evs[0])
	}
}

// T2：MT5 帧语义三连——a) 帧外仓不缺省删 b) 未知票号触发该 venue 回读 c) 已知票号字段变更 → upsert。
func TestD571_MT5FrameSemantics(t *testing.T) {
	ctx := context.Background()
	fq := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 1},
		{Venue: "v", VenueKey: "v:mt5", Ticket: "2", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4000, Profit: 2},
	}}
	q := &countingQuerier{fakeVenueQuerier: fq}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)

	// a) 帧只载 ticket 1（ticket 2 帧缺席）→ 不得发 remove，ticket 2 仍在视图。
	m.OnMT5Frame("v:mt5", []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 1},
	})
	m.flush(ctx)
	for _, ev := range drainEvents(ch) {
		if ev.GetRemove() != nil {
			t.Fatalf("frame omission must not remove, got %+v", ev.GetRemove())
		}
	}
	if len(m.Snapshot()) != 2 {
		t.Fatalf("ticket2 must survive frame omission, got %d", len(m.Snapshot()))
	}

	// b) 未知票号 → kick→REST 回读（querier 被调），且 REST 集含之 → upsert。
	before := q.callCount()
	fq.orders = append(fq.orders, venue.Order{
		Venue: "v", VenueKey: "v:mt5", Ticket: "3", Symbol: "GBPUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.3, Profit: 3,
	})
	m.OnMT5Frame("v:mt5", []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "99", Symbol: "USDJPY", Side: "BUY", Volume: 0.1, OpenPrice: 150, Profit: 0},
	})
	kicks := m.drainKicks() // D-571 R2 F-3：未知票必须 kick 本 venue（删 Kick 行此断言即红）
	if len(kicks) != 1 || kicks[0] != "v:mt5" {
		t.Fatalf("unknown ticket must kick venue v:mt5, got %v", kicks)
	}
	m.reconcileVenue(ctx, "v:mt5") // kick 信号消费在 Run 循环；测试直调等价回读
	if q.callCount() <= before {
		t.Fatalf("unknown ticket must trigger authoritative reload")
	}
	m.flush(ctx)
	found3 := false
	for _, ev := range drainEvents(ch) {
		if u := ev.GetUpsert(); u != nil && u.Ticket == "3" {
			found3 = true
		}
	}
	if !found3 {
		t.Fatalf("want upsert for ticket 3 after reload")
	}

	// c) 已知票号 profit 变更 → upsert（Pnl 变化反映在事件载荷）。
	drainEvents(ch)
	m.OnMT5Frame("v:mt5", []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 9.9},
	})
	m.flush(ctx)
	var got *v1.BrokerPosition
	for _, ev := range drainEvents(ch) {
		if u := ev.GetUpsert(); u != nil && u.Ticket == "1" {
			got = u
		}
	}
	if got == nil || got.Pnl != 9.9 {
		t.Fatalf("want upsert pnl=9.9, got %+v", got)
	}
}

// T3：指纹含现价——qbus 报价到达 → 持仓脏化 → flush 发含新 CurrentPrice 的 upsert；
// 无关 symbol 报价不触发。
func TestD571_QuoteLinkedUpsert(t *testing.T) {
	ctx := context.Background()
	q := &fakeCEXQuerier{key: "binance:perp", orders: []venue.Order{
		{Venue: "binance", VenueKey: "binance:perp", Ticket: "TRXUSDT", Symbol: "TRXUSDT", Side: "BUY", Volume: 100, OpenPrice: 0.3, Profit: 0},
	}}
	m, ch, qbus := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)

	// 无关 symbol 报价不产生脏化；相关 symbol 报价 → upsert（含新现价）。
	m.onQuote(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "perp", Bid: 60000, Ask: 60001, Time: time.Now()})
	m.flush(ctx)
	if n := len(drainEvents(ch)); n != 0 {
		t.Fatalf("unrelated quote want 0 events, got %d", n)
	}
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.30, Ask: 0.32, Time: time.Now()})
	m.onQuote(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.30, Ask: 0.32, Time: time.Now()})
	m.flush(ctx)
	evs := drainEvents(ch)
	var u *v1.BrokerPosition
	for _, ev := range evs {
		if x := ev.GetUpsert(); x != nil {
			u = x
		}
	}
	if u == nil {
		t.Fatal("want upsert after quote arrival")
	}
	if u.CurrentPrice != 0.30 || !u.Fresh { // D-009 S2：BUY 仓方向价=bid 0.30（原中价 0.31）
		t.Fatalf("want current_price=0.30 fresh, got %v fresh=%v", u.CurrentPrice, u.Fresh)
	}

	// 同样报价再来（值未变）→ proto.Equal 去重无事件。
	m.onQuote(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 0.30, Ask: 0.32, Time: time.Now()})
	m.flush(ctx)
	if n := len(drainEvents(ch)); n != 0 {
		t.Fatalf("unchanged quote want 0 events, got %d", n)
	}
}

// T4：尾沿合批——同 posKey 窗口内多次脏化只发一条（最新态胜）。
func TestD571_CoalesceTrailingEdge(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)

	// 一窗口内两帧（profit 1→2）→ 单 flush 只出最新态一条。
	m.OnMT5Frame("v:mt5", []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 1},
	})
	m.OnMT5Frame("v:mt5", []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 2},
	})
	m.flush(ctx)
	evs := drainEvents(ch)
	if len(evs) != 1 {
		t.Fatalf("want 1 coalesced event, got %d", len(evs))
	}
	if evs[0].GetUpsert().Pnl != 2 {
		t.Fatalf("want latest pnl=2, got %v", evs[0].GetUpsert().Pnl)
	}
}

// T5：回放——cur 全量每仓一条 upsert，聚合值顺带。
func TestD571_ReplayEvents(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 3},
		{Venue: "v", VenueKey: "v:mt5", Ticket: "2", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4000, Profit: 4, Swap: 1, Commission: -0.5},
	}}
	m, _, _ := newEvMonTest(t, q)
	m.Refresh(ctx)

	evs := m.Events()
	if len(evs) != 2 {
		t.Fatalf("want 2 replay events, got %d", len(evs))
	}
	seen := map[string]bool{}
	for _, ev := range evs {
		u := ev.GetUpsert()
		if u == nil {
			t.Fatalf("replay must be upsert, got %+v", ev)
		}
		seen[u.Ticket] = true
		if ev.PnlSumUsd != 7 || ev.NetSumUsd != 7.5 {
			t.Fatalf("sums want 7/7.5, got %v/%v", ev.PnlSumUsd, ev.NetSumUsd)
		}
	}
	if !seen["1"] || !seen["2"] {
		t.Fatalf("replay missing tickets: %v", seen)
	}
}

// T6：流状态承担 venue_online——OnStreamState(false) → 全仓 VenueOnline 翻转 upsert；
// 同值重复不报（幂等）。
func TestD571_StreamStateDrivesOnline(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)

	m.OnStreamState("v:mt5", false)
	m.flush(ctx)
	var got *v1.BrokerPosition
	for _, ev := range drainEvents(ch) {
		if u := ev.GetUpsert(); u != nil {
			got = u
		}
	}
	if got == nil || got.VenueOnline {
		t.Fatalf("want venue_online=false upsert, got %+v", got)
	}
	// 幂等：同值再来零事件。
	m.OnStreamState("v:mt5", false)
	m.flush(ctx)
	if n := len(drainEvents(ch)); n != 0 {
		t.Fatalf("same stream state want 0 events, got %d", n)
	}
}
