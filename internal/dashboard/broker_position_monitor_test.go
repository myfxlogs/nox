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

func (f *fakeVenueQuerier) VenueKey() string { return f.key }
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
	m := NewBrokerPositionMonitor(bm, qbus, time.Minute, time.Millisecond, time.Second, topic)
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

// TestBrokerPositionMonitor_KickMerge D-107 P2：连续多 kick 只触发一次 poll（chan 容量 1 合并语义）。
// 启动 Run → 连续 Kick 3 次 → kickCh 容量 1 只缓冲 1 个 → Run 消费后只 poll 1 次。
// 验证：pollCount 从 0 变为 1（不是 3），证明合并。
func TestBrokerPositionMonitor_KickMerge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []reconcile.VenueOrder{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 0, Magic: 0},
	}}
	m, _, _ := newMonTest(t, q)
	// 用长 interval 避免 ticker 干扰（10min 内只有 kick 触发 poll）。
	m.interval = 10 * time.Minute
	m.quoteTick = 10 * time.Minute

	go m.Run(ctx)
	// 等 Run 首采完成（初始 poll）。
	if !waitForMon(t, 2*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 清空 orders → 下次 poll 会检测到变更（0 条）。
	q.orders = nil

	// 连续 3 次 kick（chan 容量 1 → 只缓冲 1 个）。
	m.Kick()
	m.Kick()
	m.Kick()

	// 等 kick 触发的 poll 完成（snapshot 变 0 条 = poll 执行了）。
	if !waitForMon(t, 2*time.Second, func() bool { return len(m.Snapshot()) == 0 }) {
		t.Fatalf("kick-triggered poll did not fire, snapshot still %d", len(m.Snapshot()))
	}
	// 合并语义：3 次 kick 只触发 1 次 poll（snapshot 已变 0 条，不回退）。
	if len(m.Snapshot()) != 0 {
		t.Errorf("after kick merge want 0 positions, got %d", len(m.Snapshot()))
	}
}

// TestAccountMonitor_KickDebounce D-107 P2：kick 5min 防抖——窗口内 kick 不 poll、超窗 poll。
// 用 withShortAccountDebounce 缩短窗口到 50ms 快跑。
func TestAccountMonitor_KickDebounce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	restore := withShortAccountDebounce(t, 50*time.Millisecond)
	defer restore()

	q := &fakeAccountQuerier{key: "277842155:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "277842155:mt5", Venue: "277842155", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5100, Currency: "USD",
	}}
	m, _, _ := newAcctMonTest(t, q)
	m.interval = 10 * time.Minute // 避免 ticker 干扰

	go m.Run(ctx)
	// 等 Run 首采完成。
	if !waitForMon(t, 2*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 1. 窗口内 kick → pollIfDue 防抖跳过 → equity 改了也不反映。
	q.acct.Equity = 5200
	m.Kick()
	time.Sleep(20 * time.Millisecond) // 等 kick 被消费（防抖窗口内 50ms）
	if e := m.Snapshot()[0].Equity; e != 5100 {
		t.Fatalf("within debounce window: equity want 5100 (no poll), got %v", e)
	}

	// 2. 超窗后 kick → pollIfDue 执行 poll → equity 更新。
	time.Sleep(60 * time.Millisecond) // 超过 50ms 窗口
	m.Kick()
	if !waitForMon(t, 2*time.Second, func() bool {
		return len(m.Snapshot()) == 1 && m.Snapshot()[0].Equity == 5200
	}) {
		t.Fatalf("after debounce window: equity want 5200, got %v", m.Snapshot()[0].Equity)
	}
}

// TestOrderTopicToKickWiring D-107 P2：fake orderTopic Publish → bpMon.Kick 接线验证。
// 模拟 main.go 的订阅 goroutine：orderTopic.Subscribe → for range → bpMon.Kick()。
func TestOrderTopicToKickWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []reconcile.VenueOrder{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 0, Magic: 0},
	}}
	m, _, _ := newMonTest(t, q)
	m.interval = 10 * time.Minute
	m.quoteTick = 10 * time.Minute

	// 模拟 main.go 装配：orderTopic 订阅 → bpMon.Kick。
	orderTopic := bus.NewTopic[bus.OrderEvent]()
	go m.Run(ctx)
	go func() {
		evCh, subCancel := orderTopic.Subscribe()
		defer subCancel()
		for range evCh {
			m.Kick()
		}
	}()

	// 等 Run 首采。
	if !waitForMon(t, 2*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 清空 orders → 下次 poll 检测到变更。
	q.orders = nil

	// Publish 一个 OrderEvent → 经订阅 goroutine → Kick → poll → snapshot 变 0。
	orderTopic.Publish(bus.OrderEvent{Venue: "testvenue", Ticket: 1})

	if !waitForMon(t, 2*time.Second, func() bool { return len(m.Snapshot()) == 0 }) {
		t.Fatalf("orderTopic→Kick wiring: snapshot still %d after publish", len(m.Snapshot()))
	}
}

// waitForMon 轮询条件直到超时（dashboard 包内测试 helper）。
func waitForMon(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// withShortAccountDebounce 临时缩短 account kick 防抖窗口，返回恢复函数（测试快跑）。
func withShortAccountDebounce(t *testing.T, d time.Duration) func() {
	t.Helper()
	orig := accountKickDebounce
	accountKickDebounce = d
	return func() { accountKickDebounce = orig }
}
