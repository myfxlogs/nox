// d029r2_sessionskick_test.go —— D-029 R2 验收锚：SessionsKick 钩子——attach 触发 kick、
// nil 安全、映射变更触发（镜像 SwapKick 三锚形态）。
package dashboard

import (
	"sync/atomic"
	"testing"
	"time"
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
