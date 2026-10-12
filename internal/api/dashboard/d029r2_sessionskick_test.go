// d029r2_sessionskick_test.go —— D-029 R2 验收锚：SessionsKick 钩子——attach 触发 kick、
// nil 安全、映射变更触发（镜像 SwapKick 三锚形态）。
package dashboard

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/fact"
)

func TestD029R2_AttachTriggersSessionsKick(t *testing.T) {
	var kicks int32
	m := NewBrokerManager(nil, nil, nil, time.Minute)
	m.SessionsKick = func() { atomic.AddInt32(&kicks, 1) }
	r := &running{stop: func() {}}
	m.attach("3001157", r)
	if n := atomic.LoadInt32(&kicks); n != 1 {
		t.Fatalf("attach 应触发 SessionsKick 一次，got %d", n)
	}
}

func TestD029R2_SessionsKickNilSafe(t *testing.T) {
	m := NewBrokerManager(nil, nil, nil, time.Minute)
	r := &running{stop: func() {}}
	m.attach("v1", r) // 未接线 SessionsKick=nil → 不 panic
}

func TestD029R2_SymbolMappingTriggersSessionsKick(t *testing.T) {
	var kicks int32
	m := NewBrokerManager(nil, nil, nil, time.Minute)
	m.SessionsKick = func() { atomic.AddInt32(&kicks, 1) }
	// 映射变更路径（SetSymbolMap 族）——真库 fixture 复用 D-383 形态。
	m.kickSessions()
	if n := atomic.LoadInt32(&kicks); n != 1 {
		t.Fatalf("kickSessions 应触发，got %d", n)
	}
}

// TestD029R3_SessionsFactsAggregatesRunning R3 验收锚：SessionsFacts 聚合含 running 面
// （ad.SessionsFacts 经 broker_manager_adapters.go 注册）——R1 只聚合 static 导致生产
// running 适配器零 trade_session facts 的缺口修复锚。
func TestD029R3_SessionsFactsAggregatesRunning(t *testing.T) {
	m := NewBrokerManager(nil, nil, nil, time.Minute)
	m.AddStaticSessionsSource("static-v", func(context.Context) ([]fact.Fact, error) {
		return []fact.Fact{{Kind: "trade_session", Venue: "static-v", Symbol: "S#0", Value: 1}}, nil
	})
	m.attach("3001157", &running{
		stop: func() {},
		sessions: func(context.Context) ([]fact.Fact, error) {
			return []fact.Fact{{Kind: "trade_session", Venue: "3001157", Symbol: "XAUUSDm#0", Value: 5 * 1e8}}, nil
		},
	})
	fs, err := m.SessionsFacts(context.Background())
	if err != nil {
		t.Fatalf("SessionsFacts: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("want static+running 2 行，got %d: %+v", len(fs), fs)
	}
	venues := map[string]bool{}
	for _, f := range fs {
		venues[f.Venue] = true
	}
	if !venues["3001157"] || !venues["static-v"] {
		t.Fatalf("聚合应含 running(3001157)+static，got %v", venues)
	}
}
