package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/venue"
	"nox/internal/ledger/reconcile"
	"nox/internal/trading/exec"

	v1 "nox/internal/gen/dashboardv1"
)

// D-575 锚测试 T1-T3：事件热路径无界 RPC → per-call 超时闸（生产 25min 冻结止血）。

// blockingTZFetcher：ServerTimezone RPC 挂死（ctx 取消才返回）——2026-09-29 mtapi unary 劣化真形。
func blockingTZFetcher(ctx context.Context, venueKey string) (float64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// T1：dirty→AfterFunc→flush 内 tz RPC 挂死 → flush 在 bpFlushBound 界内返回且事件照发（loc 诚实空）。
func TestD575_T1_FlushBoundedOnTZHang(t *testing.T) {
	oldDelay, oldBound := bpFlushDelay, bpFlushBound
	bpFlushDelay, bpFlushBound = 10*time.Millisecond, 300*time.Millisecond
	defer func() { bpFlushDelay, bpFlushBound = oldDelay, oldBound }()

	qbus := bus.New()
	bm := NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	q := &fakeVenueQuerier{key: "v:mt5", orders: []venue.Order{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	bm.AddStaticQuerier(q.VenueKey(), q)
	topic := bus.NewTopic[*v1.BrokerPositionEvent]()
	ch, _ := topic.Subscribe()
	m := NewBrokerPositionMonitor(bm, qbus, time.Minute, topic,
		NewServerTZCache(time.Hour, blockingTZFetcher),
		exchange.NewFundingIntervalCache(), nil)

	m.reconcileAll(context.Background()) // 脏化 → AfterFunc 挂 flush

	start := time.Now()
	select {
	case ev := <-ch:
		if ev.GetUpsert() == nil {
			t.Fatalf("want upsert event, got %v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("flush 冻结——bpFlushBound 未生效（挂死复现）")
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("flush took %v，超 bpFlushBound 量级", d)
	}
}

// T2：venueOrders/venueOrderResults——QueryOpenedOrders 挂死 → per-call 10s 界内诚实 err 返回。
func TestD575_T2_QuerierBoundedOnHang(t *testing.T) {
	oldTO := monitorCallTimeout
	monitorCallTimeout = 300 * time.Millisecond
	defer func() { monitorCallTimeout = oldTO }()

	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier("v:mt5", &blockingOrderQuerier{key: "v:mt5"})

	start := time.Now()
	res := bm.venueOrderResults(context.Background())
	if len(res) != 1 || res[0].Err == nil {
		t.Fatalf("res=%+v want 1 result with err", res)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("venueOrderResults took %v——per-call 界未生效", d)
	}
	// 定向单 venue 同闸。
	start = time.Now()
	r := bm.venueOrders(context.Background(), "v:mt5")
	if r.Err == nil {
		t.Fatal("venueOrders want err")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("venueOrders took %v", d)
	}
}

// blockingOrderQuerier：QueryOpenedOrders 挂死至 ctx 取消。
type blockingOrderQuerier struct{ key string }

func (q *blockingOrderQuerier) VenueKey() string { return q.key }
func (q *blockingOrderQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// T3：acctMon Account RPC 挂死 → Refresh 有界返回（cooldown 靠超时返回才生效）。
func TestD575_T3_AcctQuerierBounded(t *testing.T) {
	oldTO := monitorCallTimeout
	monitorCallTimeout = 300 * time.Millisecond
	defer func() { monitorCallTimeout = oldTO }()

	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, bus.NewTopic[*v1.AccountEvent]())
	m.AddStatic(&blockingAcctQuerier{fakeAccountQuerier{key: "v:mt5"}})
	start := time.Now()
	m.Refresh(context.Background())
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("acct Refresh took %v——per-call 界未生效（cooldown 死锁复现）", d)
	}
}

// blockingAcctQuerier：Account 挂死至 ctx 取消。
type blockingAcctQuerier struct{ fakeAccountQuerier }

func (q *blockingAcctQuerier) Account(ctx context.Context) (*reconcile.AccountInfo, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
