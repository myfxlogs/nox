package dashboard

// D-469b T1/T4：三 manager configKick 即到即跑 + streamable okx 纳管。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

// countingExchangeStore fake store：ListExchangeConfigs 调用计数。
type countingExchangeStore struct {
	cfgs  []pgstore.ExchangeConfig
	calls int32
}

func (f *countingExchangeStore) ListExchangeConfigs(context.Context) ([]pgstore.ExchangeConfig, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.cfgs, nil
}

// T1：CexStreamManager KickTopic——publish → sync 即到即跑（不等 ticker）。
func TestD469b_StreamManagerKickSync(t *testing.T) {
	st := &countingExchangeStore{}
	m := NewCexStreamManager(st, nil, time.Hour, nil, nil) // ticker 1h——kick 是唯一触发路
	m.KickTopic = bus.NewTopic[struct{}]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&st.calls) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond) // 启动即 sync 一次
	}
	if got := atomic.LoadInt32(&st.calls); got != 1 {
		t.Fatalf("boot sync calls=%d want 1", got)
	}
	m.KickTopic.Publish(struct{}{})
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&st.calls) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&st.calls); got != 2 {
		t.Fatalf("kick sync calls=%d want 2", got)
	}
}

// T1b：CexPositionManager/CexExecutorManager 同款 kick（同构同改断言）。
func TestD469b_PosExecManagerKickSync(t *testing.T) {
	st := &countingExchangeStore{}
	posM := NewCexPositionManager(&BrokerManager{}, st, time.Hour)
	execM := NewCexExecutorManager(st, nil, time.Hour, nil, 0)
	topic := bus.NewTopic[struct{}]()
	posM.KickTopic = topic
	execM.KickTopic = topic
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go posM.Run(ctx)
	go execM.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&st.calls) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&st.calls); got != 2 {
		t.Fatalf("boot sync calls=%d want 2", got)
	}
	topic.Publish(struct{}{}) // 两订阅者扇出各 sync 一次
	deadline = time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&st.calls) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&st.calls); got != 4 {
		t.Fatalf("kick sync calls=%d want 4", got)
	}
}

// T1c：Service mutation kick 路径——SetConfigKick 注入后 kickConfig 触发一次；nil 不 panic。
func TestD469b_ServiceConfigKick(t *testing.T) {
	var s Service
	s.kickConfig() // nil 安全
	var n int
	s.SetConfigKick(func() { n++ })
	s.kickConfig()
	if n != 1 {
		t.Fatalf("kick n=%d want 1", n)
	}
}

// T4：streamable okx:perp 凭据行；缺凭据/spot/他 venue 不起。
func TestD469b_StreamableOKX(t *testing.T) {
	cases := []struct {
		c    pgstore.ExchangeConfig
		want bool
	}{
		{pgstore.ExchangeConfig{Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s", Passphrase: "p"}, true},
		{pgstore.ExchangeConfig{Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s", Passphrase: "p", Simulated: true}, true}, // demo 行同规则可起
		{pgstore.ExchangeConfig{Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s"}, false},                                  // 缺 passphrase
		{pgstore.ExchangeConfig{Venue: "okx", VenueType: "spot", APIKey: "k", APISecret: "s", Passphrase: "p"}, false},                 // spot 不起
		{pgstore.ExchangeConfig{Venue: "gate", VenueType: "perp", APIKey: "k", APISecret: "s", Passphrase: "p"}, false},                // gate 缺 BaseURL
	}
	for i, tc := range cases {
		if got := streamable(tc.c); got != tc.want {
			t.Fatalf("case %d streamable=%v want %v", i, got, tc.want)
		}
	}
}

// T4b：okx 配置行进 sync → streams 登记一行（构造器路径实走，WS 未 Run 不拨号）。
func TestD469b_SyncStartsOKX(t *testing.T) {
	st := &countingExchangeStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 7, Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s", Passphrase: "p"},
	}}
	m := NewCexStreamManager(st, nil, time.Hour, nil, nil)
	m.sync(context.Background())
	m.mu.Lock()
	n := len(m.streams)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("streams=%d want 1 (okx row)", n)
	}
	// 行消失 → stop。
	st.cfgs = nil
	m.sync(context.Background())
	m.mu.Lock()
	n = len(m.streams)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("after removal streams=%d want 0", n)
	}
}
