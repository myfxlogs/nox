package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// fakeVenueQuerier 受控 querier（D-052 monitor 测试）：orders/err 可随时切换，模拟在线/离线/空仓。
type fakeVenueQuerier struct {
	key    string
	orders []reconcile.VenueOrder
	err    error
}

func (f *fakeVenueQuerier) VenueKey() string                          { return f.key }
func (f *fakeVenueQuerier) QueryOpenedOrders(context.Context) ([]reconcile.VenueOrder, error) {
	return f.orders, f.err
}

// newMonTest 组装 monitor：nil store（BrokerSymbolMap 空 map → canonical=""，不动 broker_symbol_map 表）。
func newMonTest(t *testing.T, q *fakeVenueQuerier) (*BrokerPositionMonitor, *bus.Topic[*v1.BrokerPositionSnapshot], <-chan *v1.BrokerPositionSnapshot) {
	t.Helper()
	qbus := bus.New()
	bm := NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier("testvenue", q)
	topic := bus.NewTopic[*v1.BrokerPositionSnapshot]()
	ch, _ := topic.Subscribe()
	m := NewBrokerPositionMonitor(bm, qbus, time.Minute, time.Millisecond, topic)
	return m, topic, ch
}

// TestBrokerPositionMonitor_OfflineKeepsSnapshot：在线有仓 → 离线保留最近快照并标 venue_online=false → 恢复空仓清空（D-052）。
func TestBrokerPositionMonitor_OfflineKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []reconcile.VenueOrder{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "TRXUSDT", Side: "SELL", Volume: 0.01, OpenPrice: 0.332280, Profit: -0.01, Magic: 42},
	}}
	m, _, _ := newMonTest(t, q)

	// 1. 在线有仓 → 快照含 1 条，venue_online=true，magic 透传。
	got := m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("online snapshot want 1 position, got %d", len(got))
	}
	p := got[0]
	if !p.VenueOnline {
		t.Errorf("online venue_online want true, got false")
	}
	if p.Magic != 42 {
		t.Errorf("magic want 42, got %v", p.Magic)
	}

	// 2. 查询失败（离线）→ 保留最近快照，venue_online=false。
	q.err = errors.New("connection refused")
	got = m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("offline snapshot want retained 1 position, got %d", len(got))
	}
	if got[0].VenueOnline {
		t.Errorf("offline venue_online want false, got true")
	}

	// 3. 恢复在线且无仓 → 清空（真实权威：无持仓）。
	q.err = nil
	q.orders = nil
	got = m.Refresh(ctx)
	if len(got) != 0 {
		t.Fatalf("recovered empty want 0 positions, got %d", len(got))
	}
}

// TestBrokerPositionMonitor_BroadcastOnChangeOnly：仅变更广播；无变化不重复推（指纹去重）。
func TestBrokerPositionMonitor_BroadcastOnChangeOnly(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []reconcile.VenueOrder{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 0, Magic: 0},
	}}
	m, _, ch := newMonTest(t, q)

	drain := func() int { // 取走缓冲中的快照数
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

	if n := drain(); n != 0 {
		t.Fatalf("before any poll want 0 broadcasts, got %d", n)
	}
	m.Refresh(ctx) // 首次：空 → 有仓，变更
	if n := drain(); n != 1 {
		t.Fatalf("first poll want 1 broadcast, got %d", n)
	}
	m.Refresh(ctx) // 无变化
	if n := drain(); n != 0 {
		t.Fatalf("no-change poll want 0 broadcasts, got %d", n)
	}

	q.orders[0].Volume = 0.2 // 变更 → 再广播
	m.Refresh(ctx)
	if n := drain(); n != 1 {
		t.Fatalf("change poll want 1 broadcast, got %d", n)
	}
}
