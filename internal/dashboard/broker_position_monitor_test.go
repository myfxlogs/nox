package dashboard

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	"arb/internal/exec"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
	"arb/internal/venue"

	v1 "arb/internal/gen/dashboardv1"
)

// fakeVenueQuerier 受控 querier（D-052 monitor 测试）：orders/err 可随时切换，模拟在线/离线/空仓。
type fakeVenueQuerier struct {
	key      string
	orders   []venue.Order
	err      error
	tzOffset float64
	tzErr    error
	calls    atomic.Int64 // D-094：回读调用计数（boot 单次拉取锚）
}

func (f *fakeVenueQuerier) VenueKey() string { return f.key }
func (f *fakeVenueQuerier) QueryOpenedOrders(context.Context) ([]venue.Order, error) {
	f.calls.Add(1)
	return f.orders, f.err
}

// ServerTimezone D-337 S2 可选接口实现（fake 可控 offset/err）。
func (f *fakeVenueQuerier) ServerTimezone(context.Context) (float64, error) {
	if f.tzErr != nil {
		return 0, f.tzErr
	}
	return f.tzOffset, nil
}

// newMonTest 组装 monitor：nil store（BrokerSymbolMap 空 map → canonical=""，不动 broker_symbol_map 表）。
func newMonTest(t *testing.T, q *fakeVenueQuerier) (*BrokerPositionMonitor, *bus.Topic[*v1.BrokerPositionEvent], <-chan *v1.BrokerPositionEvent) {
	t.Helper()
	qbus := bus.New()
	bm := NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier("testvenue", q)
	topic := bus.NewTopic[*v1.BrokerPositionEvent]()
	ch, _ := topic.Subscribe()
	m := NewBrokerPositionMonitor(bm, qbus, time.Minute, topic,
		NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) { return 0, errors.New("n/a") }),
		exchange.NewFundingIntervalCache(), nil)
	return m, topic, ch
}

// TestBrokerPositionMonitor_OfflineKeepsSnapshot：在线有仓 → 离线保留最近快照并标 venue_online=false → 恢复空仓清空（D-052）。
func TestBrokerPositionMonitor_OfflineKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
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
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
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

	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 0, Magic: 0},
	}}
	m, _, _ := newMonTest(t, q)

	go m.Run(ctx)
	// 等 Run 首采完成（初始 poll）。
	if !waitForMon(t, 10*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 清空 orders → 下次 poll 会检测到变更（0 条）。
	q.orders = nil

	// 连续 3 次 kick（chan 容量 1 → 只缓冲 1 个）。
	m.Kick("testvenue:mt5")
	m.Kick("testvenue:mt5")
	m.Kick("testvenue:mt5")

	// 等 kick 触发的 poll 完成（snapshot 变 0 条 = poll 执行了）。
	if !waitForMon(t, 10*time.Second, func() bool { return len(m.Snapshot()) == 0 }) {
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

	go m.Run(ctx)
	// 等 Run 首采完成。
	if !waitForMon(t, 10*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 1. 窗口内 kick → pollIfDue 防抖跳过 → equity 改了也不反映。
	q.acct.Equity = 5200
	m.Kick("")
	time.Sleep(20 * time.Millisecond) // 等 kick 被消费（防抖窗口内 50ms）
	if e := m.Snapshot()[0].Equity; e != 5100 {
		t.Fatalf("within debounce window: equity want 5100 (no poll), got %v", e)
	}

	// 2. 超窗后 kick → pollIfDue 执行 poll → equity 更新。
	time.Sleep(60 * time.Millisecond) // 超过 50ms 窗口
	m.Kick("")
	if !waitForMon(t, 10*time.Second, func() bool {
		return len(m.Snapshot()) == 1 && m.Snapshot()[0].Equity == 5200
	}) {
		t.Fatalf("after debounce window: equity want 5200, got %v", m.Snapshot()[0].Equity)
	}
}

// TestOrderTopicToKickWiring D-107 P2：fake orderTopic Publish → bpMon.Kick 接线验证。
// 模拟 main.go 的订阅 goroutine：orderTopic.Subscribe → for range → bpMon.Kick()。
// D-249 S5 R-G5：等待窗口 10s→45s——-race 全仓并行负载下事件可能晚到 10s+（CI 实证 flake，
// 失败后 1s 内事件到达），45s 窗口给 race detector 足够余量，late-arrival 属调度噪声非缺陷。
// D-316④：45s→120s——共享主机峰值负载下订阅/Kick goroutine 饥饿可超 45s（全量包 45.00s
// 整点超时实证一次，master 隔离绿）；单发 Publish 入空缓冲 1 必达不丢（topic.go 丢旧保新
// 只在缓冲满时发生），等待预算继续沿「晚到非缺陷」策略放大。
func TestOrderTopicToKickWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 0, Magic: 0},
	}}
	m, _, _ := newMonTest(t, q)
	// 模拟 main.go 装配：orderTopic 订阅 → bpMon.Kick。
	// 测试内订阅循环必须 select ctx.Done（D-207：防泄漏 goroutine 持池连接→死锁/EBADF）。
	orderTopic := bus.NewTopic[bus.OrderEvent]()
	go m.Run(ctx)
	go func() {
		evCh, subCancel := orderTopic.Subscribe()
		defer subCancel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-evCh:
				if !ok {
					return
				}
				m.Kick("testvenue:mt5")
			}
		}
	}()

	// 等 Run 首采（D-316④：120s 窗口，共享主机峰值负载下首采可能延迟）。
	if !waitForMon(t, 120*time.Second, func() bool { return len(m.Snapshot()) == 1 }) {
		t.Fatal("initial poll did not produce snapshot")
	}

	// 清空 orders → 下次 poll 检测到变更。
	q.orders = nil

	// Publish 一个 OrderEvent → 经订阅 goroutine → Kick → poll → snapshot 变 0。
	// D-316④：120s 窗口——Publish→Subscribe→Kick→poll 链路在共享主机峰值负载下可晚到
	// （单发事件入空缓冲 1 必达不丢），late-arrival 属调度噪声非缺陷。
	orderTopic.Publish(bus.OrderEvent{Venue: "testvenue", Ticket: 1})

	if !waitForMon(t, 120*time.Second, func() bool { return len(m.Snapshot()) == 0 }) {
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

// --- D-110 P2: swap 快照采集测试 ---

// fakeSwapSnapStore 受控 SwapSnapshotStore（记录 InsertSwapSnapshot 调用）。
type fakeSwapSnapStore struct {
	inserts   []fakeSwapInsert
	positions []pgstore.Position
	legsByPos map[int64][]pgstore.PositionLeg
}

type fakeSwapInsert struct {
	PosID   int64
	Venue   string
	SwapUSD float64
}

func (f *fakeSwapSnapStore) InsertSwapSnapshot(_ context.Context, posID int64, venue string, swapUSD float64, _ time.Time) error {
	f.inserts = append(f.inserts, fakeSwapInsert{posID, venue, swapUSD})
	return nil
}
func (f *fakeSwapSnapStore) ListPositions(_ context.Context, status string, _, _ int) ([]pgstore.Position, error) {
	if status != "ACTIVE" && status != "OPEN" {
		return nil, nil
	}
	return f.positions, nil
}
func (f *fakeSwapSnapStore) ListPositionLegs(_ context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legsByPos[posID], nil
}

// TestCollectSwapSnapshots_SwapChangeWrites D-110 P2：swap 值变化 → InsertSwapSnapshot 被调用。
func TestCollectSwapSnapshots_SwapChangeWrites(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "venue:mt5", orders: []venue.Order{
		{Venue: "venue", VenueKey: "venue:mt5", Ticket: "T1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Swap: 5.0},
	}}
	m, _, _ := newMonTest(t, q)
	fake := &fakeSwapSnapStore{
		positions: []pgstore.Position{{ID: 100, Status: "OPEN"}},
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {{Phase: "OPEN", Status: "FILLED", Venue: "venue:mt5", OrderID: "T1"}},
		},
	}
	m.SetSwapSnapshotStore(fake)
	m.Refresh(ctx) // 首次 poll → swap=5.0 首次写
	if len(fake.inserts) != 1 {
		t.Fatalf("first poll: want 1 insert, got %d", len(fake.inserts))
	}
	if fake.inserts[0].SwapUSD != 5.0 {
		t.Fatalf("want swap=5.0, got %.4f", fake.inserts[0].SwapUSD)
	}
	// swap 值不变 → 不写
	m.Refresh(ctx)
	if len(fake.inserts) != 1 {
		t.Fatalf("no-change poll: want 1 insert, got %d", len(fake.inserts))
	}
	// swap 值变化 → 写
	q.orders[0].Swap = 6.0
	m.Refresh(ctx)
	if len(fake.inserts) != 2 {
		t.Fatalf("change poll: want 2 inserts, got %d", len(fake.inserts))
	}
	if fake.inserts[1].SwapUSD != 6.0 {
		t.Fatalf("want swap=6.0, got %.4f", fake.inserts[1].SwapUSD)
	}
}

// TestCollectSwapSnapshots_SwapZeroFirstWrite D-110 P2 B2：swap=0 + 首次 → 写入。
func TestCollectSwapSnapshots_SwapZeroFirstWrite(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "venue:mt5", orders: []venue.Order{
		{Venue: "venue", VenueKey: "venue:mt5", Ticket: "T1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Swap: 0},
	}}
	m, _, _ := newMonTest(t, q)
	fake := &fakeSwapSnapStore{
		positions: []pgstore.Position{{ID: 100, Status: "OPEN"}},
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {{Phase: "OPEN", Status: "FILLED", Venue: "venue:mt5", OrderID: "T1"}},
		},
	}
	m.SetSwapSnapshotStore(fake)
	m.Refresh(ctx)
	if len(fake.inserts) != 1 {
		t.Fatalf("first poll swap=0: want 1 insert, got %d", len(fake.inserts))
	}
	if fake.inserts[0].SwapUSD != 0 {
		t.Fatalf("want swap=0, got %.4f", fake.inserts[0].SwapUSD)
	}
	// 再次 poll swap=0 → 去重不写
	m.Refresh(ctx)
	if len(fake.inserts) != 1 {
		t.Fatalf("no-change swap=0: want 1 insert, got %d", len(fake.inserts))
	}
}

// TestCollectSwapSnapshots_PositionIDZeroSkips D-110 P2：position_id=0（孤儿单）→ 不写。
func TestCollectSwapSnapshots_PositionIDZeroSkips(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "venue:mt5", orders: []venue.Order{
		{Venue: "venue", VenueKey: "venue:mt5", Ticket: "ORPHAN", Symbol: "EURUSD", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Swap: 5.0},
	}}
	m, _, _ := newMonTest(t, q)
	fake := &fakeSwapSnapStore{
		positions: []pgstore.Position{{ID: 100, Status: "OPEN"}},
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {{Phase: "OPEN", Venue: "venue:mt5", OrderID: "OTHER"}}, // 不匹配 ORPHAN
		},
	}
	m.SetSwapSnapshotStore(fake)
	m.Refresh(ctx)
	if len(fake.inserts) != 0 {
		t.Fatalf("orphan order: want 0 inserts, got %d", len(fake.inserts))
	}
}

// TestD337_BuildSwapChargeAt T3：同 venue 订单 swap_charge_at 相同且有值；
// 时区获取失败 venue → nil（诚实空态）。
func TestD337_BuildSwapChargeAt(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4000, Profit: 0},
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "2", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4001, Profit: 0},
	}}
	qb1 := bus.New()
	bm1 := NewBrokerManager(nil, qb1, exec.NewRegistry(), time.Minute)
	bm1.AddStaticQuerier(q.key, q)
	m := NewBrokerPositionMonitor(bm1, qb1, time.Minute,
		bus.NewTopic[*v1.BrokerPositionEvent](),
		NewServerTZCache(time.Hour, func(_ context.Context, _ string) (float64, error) {
			return 3, nil // EEST 形态
		}), exchange.NewFundingIntervalCache(), nil)
	got := m.Refresh(ctx)
	if len(got) != 2 {
		t.Fatalf("want 2 positions, got %d", len(got))
	}
	if got[0].SwapChargeAt == nil || got[1].SwapChargeAt == nil {
		t.Fatalf("swap_charge_at 应有值（红=现码无填充）, got %v / %v", got[0].SwapChargeAt, got[1].SwapChargeAt)
	}
	if !got[0].SwapChargeAt.AsTime().Equal(got[1].SwapChargeAt.AsTime()) {
		t.Errorf("同 venue 两单应同一计费时刻: %v vs %v", got[0].SwapChargeAt, got[1].SwapChargeAt)
	}
	// 时区获取失败 venue → nil
	q2 := &fakeVenueQuerier{key: "v2:mt5", orders: []venue.Order{
		{Venue: "v2", VenueKey: "v2:mt5", Ticket: "9", Symbol: "XAUUSD", Side: "SELL", Volume: 0.01, OpenPrice: 4000, Profit: 0},
	}}
	qb2 := bus.New()
	bm2 := NewBrokerManager(nil, qb2, exec.NewRegistry(), time.Minute)
	bm2.AddStaticQuerier(q2.key, q2)
	m2 := NewBrokerPositionMonitor(bm2, qb2, time.Minute,
		bus.NewTopic[*v1.BrokerPositionEvent](),
		NewServerTZCache(time.Hour, func(_ context.Context, _ string) (float64, error) {
			return 0, errors.New("rpc down")
		}), exchange.NewFundingIntervalCache(), nil)
	got2 := m2.Refresh(ctx)
	if len(got2) != 1 || got2[0].SwapChargeAt != nil {
		t.Errorf("无时区 venue want nil, got %d/%v", len(got2), got2[0].SwapChargeAt)
	}
}
