package dashboard

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"
)

// countingAcctQuerier 包装 fakeAccountQuerier 记 Account() 调用数（帧/kick 零 REST 断言用）。
type countingAcctQuerier struct {
	fakeAccountQuerier
	calls atomic.Int32
}

func (c *countingAcctQuerier) Account(context.Context) (*reconcile.AccountInfo, error) {
	c.calls.Add(1)
	return c.acct, c.err
}

// acctEventCollector 后台收集事件（Topic cap1 丢旧保新——断言多条必须边发边收）。
// drainAcctEvents 取走事件 chan 缓冲（topic cap1 丢旧保新，逐条收——镜像 d571 drainEvents）。
func drainAcctEvents(ch <-chan *v1.AccountEvent) []*v1.AccountEvent {
	var out []*v1.AccountEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func newAcctMonV1(t *testing.T, qs ...reconcile.AccountQuerier) (*AccountMonitor, *bus.Topic[*v1.AccountEvent], <-chan *v1.AccountEvent) {
	t.Helper()
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	topic := bus.NewTopic[*v1.AccountEvent]()
	m := NewAccountMonitor(bm, topic)
	for _, q := range qs {
		m.AddStatic(q)
	}
	ch, _ := topic.Subscribe()
	return m, topic, ch
}

// T1：逐 venue upsert 事件（topic cap1 丢旧保新——逐 venue 定向 poll 各取一条断言键/值）。
func TestD571B_T1_PollPerVenueEvents(t *testing.T) {
	ctx := context.Background()
	m, _, ch := newAcctMonV1(t,
		&fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}},
		&fakeAccountQuerier{key: "277842155:mt5", acct: &reconcile.AccountInfo{VenueKey: "277842155:mt5", Equity: 200}},
		&fakeAccountQuerier{key: "gate:perp", acct: &reconcile.AccountInfo{VenueKey: "gate:perp", Equity: 300}},
	)
	for _, tc := range []struct {
		kick, vk string
		eq       float64
	}{
		{"binance", "binance:spot", 100},
		{"277842155", "277842155:mt5", 200},
		{"gate:perp", "gate:perp", 300},
	} {
		m.pollVenueKeys(ctx, tc.kick)
		evs := drainAcctEvents(ch)
		if len(evs) != 1 {
			t.Fatalf("%s events=%d want 1", tc.vk, len(evs))
		}
		acct := evs[0].GetAccount()
		if acct == nil || acct.GetVenueKey() != tc.vk || acct.GetEquity() != tc.eq {
			t.Fatalf("ev %+v want venue=%s eq=%v", evs[0], tc.vk, tc.eq)
		}
	}
	// 同值重 poll → proto.Equal 抑制零事件。
	m.pollVenueKeys(ctx, "binance")
	if evs := drainAcctEvents(ch); len(evs) != 0 {
		t.Fatalf("no-change poll emitted %d events", len(evs))
	}
}

// T2：同一事件源三次脏化 → 恰一条 upsert 事件（proto.Equal 抑制+合批）。
func TestD571B_T2_DupSuppressed(t *testing.T) {
	q := &fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}}
	m, _, ch := newAcctMonV1(t, q)
	now := time.Now()
	for i := 0; i < 3; i++ {
		m.ObserveStreamAccount("binance:spot", 100, 100, 10, 90, 0, 0, "USDT", now)
	}
	m.flush(context.Background())
	if evs := drainAcctEvents(ch); len(evs) != 1 {
		t.Fatalf("events=%d want 1", len(evs))
	}
}

// T3：MT5 帧 Push 六字段落态零 REST；空 Currency 帧不盖已有币种。
func TestD571B_T3_StreamPatchNoREST(t *testing.T) {
	q := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{key: "277842155:mt5",
		acct: &reconcile.AccountInfo{VenueKey: "277842155:mt5", Equity: 1000, FreeMargin: 900, Currency: "USD"}}}
	m, _, ch := newAcctMonV1(t, q)
	m.Refresh(context.Background())
	drainAcctEvents(ch)
	m.ObserveStreamAccount("277842155:mt5", 1100, 1200, 50, 950, 5, 2400, "", time.Now())
	m.flush(context.Background())
	evs := drainAcctEvents(ch)
	if len(evs) != 1 {
		t.Fatalf("events=%d want 1", len(evs))
	}
	acct := evs[0].GetAccount()
	if acct.GetEquity() != 1200 || acct.GetFreeMargin() != 950 {
		t.Fatalf("frame fields not applied: %+v", acct)
	}
	if acct.GetCurrency() != "USD" {
		t.Fatalf("blank frame currency overwrote: %q", acct.GetCurrency())
	}
	if q.calls.Load() != 1 {
		t.Fatalf("calls=%d want 1 (initial poll only)", q.calls.Load())
	}
}

// T4：定向 kick → REST 回读本 venue 一次（撞键 #id 不输家语义：精确键+裸键匹配）。
func TestD571B_T4_TargetedKickREST(t *testing.T) {
	qb := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{key: "binance:spot",
		acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}}}
	qg := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{key: "gate:perp",
		acct: &reconcile.AccountInfo{VenueKey: "gate:perp", Equity: 200}}}
	m, _, _ := newAcctMonV1(t, qb, qg)
	m.pollVenueKeys(context.Background(), "binance")
	if qb.calls.Load() != 1 || qg.calls.Load() != 0 {
		t.Fatalf("bare kick calls binance=%d gate=%d want 1,0", qb.calls.Load(), qg.calls.Load())
	}
	m.pollVenueKeys(context.Background(), "gate:perp")
	if qb.calls.Load() != 1 || qg.calls.Load() != 1 {
		t.Fatalf("full kick calls binance=%d gate=%d want 1,1", qb.calls.Load(), qg.calls.Load())
	}
}

// T5：Kick("") 全量防抖 + Kick(vk) 定向直读（经 Run 循环 kickSig 消费）。
func TestD571B_T5_KickViaRun(t *testing.T) {
	qb := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{key: "binance:spot",
		acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}}}
	qg := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{key: "gate:perp",
		acct: &reconcile.AccountInfo{VenueKey: "gate:perp", Equity: 200}}}
	m, _, _ := newAcctMonV1(t, qb, qg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := withShortAccountDebounce(t, 20*time.Millisecond)
	defer restore()
	go m.Run(ctx)
	waitFor(t, time.Second, func() bool { return qb.calls.Load() >= 1 && qg.calls.Load() >= 1 }) // Run 启动 poll
	m.Kick("gate:perp")
	waitFor(t, time.Second, func() bool { return qg.calls.Load() >= 2 })
	if qb.calls.Load() != 1 {
		t.Fatalf("targeted kick touched binance calls=%d", qb.calls.Load())
	}
	time.Sleep(30 * time.Millisecond) // 越过 20ms 防抖窗（基准=Run 启动 poll）
	m.Kick("")
	waitFor(t, time.Second, func() bool { return qb.calls.Load() >= 2 && qg.calls.Load() >= 3 })
}

// T6：REST 失败保旧值不发新数据事件（离线翻转发一次 online=false）。
func TestD571B_T6_FailKeepsSnapshot(t *testing.T) {
	q := &fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}}
	m, _, ch := newAcctMonV1(t, q)
	m.Refresh(context.Background())
	drainAcctEvents(ch)
	q.err = errors.New("boom")
	m.Refresh(context.Background())
	// 失败保留最近成功快照——脏不毒化：若 online 态翻转则一条 online=false；
	// 不翻转则零事件。两形下值都必须保 100。
	for _, ev := range drainAcctEvents(ch) {
		if ev.GetAccount().GetEquity() != 100 {
			t.Fatalf("failure corrupted snapshot: %+v", ev)
		}
	}
	m.Refresh(context.Background())
	for _, ev := range drainAcctEvents(ch) {
		if ev.GetAccount().GetEquity() != 100 {
			t.Fatalf("repeated failure corrupted snapshot: %+v", ev)
		}
	}
}

// T7：回放源=当前全集逐 venue upsert（streamInitAccounts 语义）。
func TestD571B_T7_ReplayFullSet(t *testing.T) {
	m, _, _ := newAcctMonV1(t,
		&fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{VenueKey: "binance:spot", Equity: 100}},
		&fakeAccountQuerier{key: "gate:perp", acct: &reconcile.AccountInfo{VenueKey: "gate:perp", Equity: 200}},
	)
	m.Refresh(context.Background())
	evs := m.Events()
	if len(evs) != 2 {
		t.Fatalf("replay events=%d want 2", len(evs))
	}
	for _, ev := range evs {
		if ev.GetAccount() == nil {
			t.Fatalf("replay ev %+v", ev)
		}
	}
}

func waitFor(t *testing.T, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waitFor timeout")
}
