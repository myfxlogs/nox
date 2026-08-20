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

// fakeAccountQuerier 受控账户查询器（D-053 monitor 测试）：acct/err 可随时切换。
type fakeAccountQuerier struct {
	key  string
	acct *reconcile.AccountInfo
	err  error
}

func (f *fakeAccountQuerier) VenueKey() string { return f.key }
func (f *fakeAccountQuerier) Account(context.Context) (*reconcile.AccountInfo, error) {
	return f.acct, f.err
}

// newAcctMonTest 组装 AccountMonitor：nil store + 静态 fake 查询器。
func newAcctMonTest(t *testing.T, q *fakeAccountQuerier) (*AccountMonitor, *bus.Topic[*v1.AccountSnapshot], <-chan *v1.AccountSnapshot) {
	t.Helper()
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	topic := bus.NewTopic[*v1.AccountSnapshot]()
	ch, _ := topic.Subscribe()
	m := NewAccountMonitor(bm, time.Millisecond, topic)
	m.AddStatic(q)
	return m, topic, ch
}

// TestAccountMonitor_OfflineKeepsSnapshot：在线有值 → 离线保留最近快照并标 online=false → 恢复更新（D-053）。
func TestAccountMonitor_OfflineKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{
		VenueKey: "binance:spot", Venue: "binance", VenueType: "spot", AssetType: "cex",
		Balance: 1000, Equity: 1000, Currency: "USDT",
	}}
	m, _, _ := newAcctMonTest(t, q)

	got := m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("online snapshot want 1 account, got %d", len(got))
	}
	if !got[0].Online {
		t.Errorf("online want Online=true, got false")
	}
	if got[0].Equity != 1000 {
		t.Errorf("equity want 1000, got %v", got[0].Equity)
	}

	// 离线 → 保留最近快照，online=false。
	q.err = errors.New("connection refused")
	got = m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("offline snapshot want retained 1 account, got %d", len(got))
	}
	if got[0].Online {
		t.Errorf("offline want Online=false, got true")
	}
	if got[0].Equity != 1000 {
		t.Errorf("offline retained equity want 1000, got %v", got[0].Equity)
	}

	// 恢复在线（权益变化）→ 更新。
	q.err = nil
	q.acct.Equity = 1100
	q.acct.Balance = 1100
	got = m.Refresh(ctx)
	if len(got) != 1 || got[0].Equity != 1100 {
		t.Fatalf("recovered want equity 1100, got %+v", got)
	}
	if !got[0].Online {
		t.Errorf("recovered want Online=true")
	}
}

// TestAccountMonitor_BroadcastOnChangeOnly：仅变更广播；权益变化也广播（指纹含值），无变化不重复推。
func TestAccountMonitor_BroadcastOnChangeOnly(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "277842155:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "277842155:mt5", Venue: "277842155", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5100, FreeMargin: 2000, Currency: "USD",
	}}
	m, _, ch := newAcctMonTest(t, q)

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

	if n := drain(); n != 0 {
		t.Fatalf("before any poll want 0 broadcasts, got %d", n)
	}
	m.Refresh(ctx) // 首次：空 → 有值，变更
	if n := drain(); n != 1 {
		t.Fatalf("first poll want 1 broadcast, got %d", n)
	}
	m.Refresh(ctx) // 无变化
	if n := drain(); n != 0 {
		t.Fatalf("no-change poll want 0 broadcasts, got %d", n)
	}

	q.acct.Equity = 5120 // 权益跳变 → 再广播
	m.Refresh(ctx)
	if n := drain(); n != 1 {
		t.Fatalf("equity-change poll want 1 broadcast, got %d", n)
	}
}
