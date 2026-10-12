package dashboard

import (
	"nox/internal/data/venue"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// T1①：晚绑定回归——模拟 startMT5 先拷贝 dispatch*（bound-method），Set* 晚接后
// 已拷 dispatcher 必须送达最新目标（本 bug 的直接形态：拷到字段快照=永久 nil）。
func TestBrokerManager_LateBindPositions(t *testing.T) {
	m := &BrokerManager{}
	copied := m.dispatchPositions // startMT5 拷贝点（先于 Set*）
	var got atomic.Int32
	m.SetPositionsSink(func(vk string, orders []venue.Order) {
		if vk == "v:mt5" {
			got.Add(1)
		}
	})
	copied("v:mt5", []venue.Order{{Ticket: "1", Venue: "v"}})
	if got.Load() != 1 {
		t.Fatalf("late-bound positions sink not reached: %d", got.Load())
	}
}

// T1②：dispatchAccount/dispatchStreamState/dispatchOrderProfitKick 同型晚绑定。
func TestBrokerManager_LateBindOthers(t *testing.T) {
	m := &BrokerManager{}
	da := m.dispatchAccount
	ds := m.dispatchStreamState
	var acct, state atomic.Int32
	m.SetAccountSink(func(vk string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) { acct.Add(1) })
	m.SetStreamStateSink(func(vk string, on bool) { state.Add(1) })
	da("v:mt5", 1, 2, 3, 4, 5, 6, "USD", time.Now())
	ds("v:mt5", true)
	if acct.Load() != 1 || state.Load() != 1 {
		t.Fatalf("late-bound sinks not reached: acct=%d state=%d", acct.Load(), state.Load())
	}
	var kicks atomic.Int32
	var kickVK atomic.Value
	m.SetOrderProfitKick(func(vk string) { kickVK.Store(vk); kicks.Add(1) })
	if k := m.OrderProfitKickFn(); k != nil {
		k("v:mt5")
	}
	if kicks.Load() != 1 || kickVK.Load() != "v:mt5" {
		t.Fatalf("orderProfitKick not reached: %d vk=%v", kicks.Load(), kickVK.Load())
	}
}

// T1③：包覆链——Set base → StreamStateSinkFn() 取出 → Set wrap → dispatch 两者按序皆调
// （wire_posmon prevStateSink 链形态）。
func TestBrokerManager_StreamStateWrapChain(t *testing.T) {
	m := &BrokerManager{}
	var seq []string
	var mu sync.Mutex
	m.SetStreamStateSink(func(vk string, on bool) { mu.Lock(); seq = append(seq, "base"); mu.Unlock() })
	prev := m.StreamStateSinkFn()
	m.SetStreamStateSink(func(vk string, on bool) {
		if prev != nil {
			prev(vk, on)
		}
		mu.Lock()
		seq = append(seq, "wrap")
		mu.Unlock()
	})
	m.dispatchStreamState("v:mt5", true)
	mu.Lock()
	defer mu.Unlock()
	if len(seq) != 2 || seq[0] != "base" || seq[1] != "wrap" {
		t.Fatalf("wrap chain order: %v", seq)
	}
}

// T1④：全 dispatcher 目标未接时调用零 panic（原 nil 语义保持）。
func TestBrokerManager_DispatchNilSafe(t *testing.T) {
	m := &BrokerManager{}
	m.dispatchPositions("v:mt5", nil)
	m.dispatchStreamState("v:mt5", false)
	m.dispatchAccount("v:mt5", 0, 0, 0, 0, 0, 0, "", time.Time{})
	if k := m.OrderProfitKickFn(); k != nil {
		t.Fatal("expected nil kick")
	}
	if f := m.StreamStateSinkFn(); f != nil {
		t.Fatal("expected nil streamStateSink")
	}
}

// T2：并发竞态——N goroutine 交替 Set* / dispatch*，-race 下零报告。
func TestBrokerManager_SinkRace(t *testing.T) {
	m := &BrokerManager{}
	var calls atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.SetPositionsSink(func(string, []venue.Order) { calls.Add(1) })
				m.SetStreamStateSink(func(string, bool) {})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.dispatchPositions("v:mt5", nil)
				m.dispatchStreamState("v:mt5", true)
				if k := m.OrderProfitKickFn(); k != nil {
					k("v:mt5")
				}
			}
		}()
	}
	wg.Wait()
}
