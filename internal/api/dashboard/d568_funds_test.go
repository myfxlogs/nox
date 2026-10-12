package dashboard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/ledger/reconcile"
	"arb/internal/trading/exec"
)

// TestD568_ObserveStreamAccountNotifies T2a：MT5 余额帧 → fundsHook 触发。
func TestD568_ObserveStreamAccountNotifies(t *testing.T) {
	m := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), nil)
	var hits atomic.Int32
	m.SetFundsHook(func() { hits.Add(1) })
	m.ObserveStreamAccount("3001157:mt5", 1, 1, 0, 0, 0, 0, "USD", time.Now())
	if hits.Load() != 1 {
		t.Fatalf("stream frame want hook once, got %d", hits.Load())
	}
}

// TestD568_PollChangedNotifies T2b：poll changed → hook 触；无变化 poll 不触。
func TestD568_PollChangedNotifies(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{
		VenueKey: "binance:spot", Venue: "binance", VenueType: "spot", AssetType: "cex",
		Balance: 1000, Equity: 1000, Currency: "USDT",
	}}
	m, _, _ := newAcctMonTest(t, q)
	var hits atomic.Int32
	m.SetFundsHook(func() { hits.Add(1) })

	m.Refresh(ctx) // 首次：空→有值 = changed
	if hits.Load() != 1 {
		t.Fatalf("changed poll want hook once, got %d", hits.Load())
	}
	m.Refresh(ctx) // 无变化 → 不触
	if hits.Load() != 1 {
		t.Fatalf("unchanged poll want hook still 1, got %d", hits.Load())
	}
	q.acct.Equity = 1100 // 权益变 → changed
	m.Refresh(ctx)
	if hits.Load() != 2 {
		t.Fatalf("equity-change poll want hook 2, got %d", hits.Load())
	}
}

// TestD568_NotifyFundsThrottle T3：突发 10 次 → publish ≤2（首即发+尾沿一）；
// 间隔 >2s 后再调 → 再发；nil refreshTopic 不 panic。
func TestD568_NotifyFundsThrottle(t *testing.T) {
	var s Service // refreshTopic nil：不 panic 兜底
	s.NotifyFunds()

	topic := bus.NewTopic[string]()
	ch, _ := topic.Subscribe()
	s2 := &Service{refreshTopic: topic}

	drain := func() int {
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

	for i := 0; i < 10; i++ {
		s2.NotifyFunds()
	}
	time.Sleep(50 * time.Millisecond) // 首即发已落
	if n := drain(); n != 1 {
		t.Fatalf("burst want 1 immediate publish, got %d", n)
	}
	time.Sleep(fundsNotifyInterval + 200*time.Millisecond) // 等尾沿冲刷
	if n := drain(); n > 1 {
		t.Fatalf("burst want trailing ≤1 publish, got %d", n)
	}
	time.Sleep(fundsNotifyInterval + 100*time.Millisecond) // 尾沿也记 last——等过窗口再调才即
	s2.NotifyFunds()
	if n := drain(); n != 1 {
		t.Fatalf("after interval want 1 publish, got %d", n)
	}
}
