// kick_helpers_test.go —— newKickTestEngine 共享具（engine_kick_test 挂起 M2.5，
// dashboard.Service 依赖换 d014StubSvc 空壳——kick/watchdog 径不触 service 方法）。
package autotrade

import (
	"context"
	"testing"

	"arb/internal/data/bus"
	"arb/internal/risk/gate"
)

// newKickTestEngine 构造带 DB store 的 Engine（空表，scanAndClose/stateMachineTick 快速返回）。
// P4→D-004 P1#4 单测共用：Kick 合并 / kick 合流 / watchdog 看门狗。
func newKickTestEngine(t *testing.T) *Engine {
	t.Helper()
	s := testAccumStore(t)
	// 清空表防串扰。
	if _, err := s.Pool().Exec(context.Background(), `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 初始化 autotrade_state（stateMachineTick 依赖 GetAutotradeState）。
	if err := s.SetAutotradeState(context.Background(), gate.StateRunning, "test", nil); err != nil {
		t.Fatalf("set autotrade state: %v", err)
	}
	return &Engine{
		cfg:             gate.Config{Enabled: true},
		store:           s,
		service:         d014StubSvc{},
		kickCh:          make(chan struct{}, 1),
		quoteCh:         make(chan bus.Quote, 256),
		carryKick:       make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
	}
}
