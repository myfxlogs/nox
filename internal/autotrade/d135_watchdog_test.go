package autotrade

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// newD135Engine 判据单测用 Engine（仅 chan 面，不跑 loop）。
func newD135Engine() *Engine {
	return &Engine{
		kickCh:    make(chan struct{}, 1),
		carryKick: make(chan struct{}, 1),
	}
}

// TestD135_ExitEvalStarved 队列驻留判据锚（D-135 验收）：
// 饥荒=有持仓 ∧ kick 在 chan 内驻留 >30s 未取件；安静窗/消费窗/无仓 → 不饥荒。
// 65121 误报形态（kick 已取件、runFull 消化中、pendingAt 已清）= len==0 → 天然不告警。
func TestD135_ExitEvalStarved(t *testing.T) {
	e := newD135Engine()
	e.openCount.Store(1)

	// 无 kick（安静窗）→ 不饥荒
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("quiet window → must not starve")
	}
	// kick 刚入队（驻留 <30s，正常消费窗）→ 不饥荒
	e.Kick()
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("fresh queued kick (ms-level dwell) → must not starve")
	}
	// kick 驻留超界（loop 卡死/阻塞）→ 饥荒
	e.kickPendingAt.Store(time.Now().Add(-exitEvalQueueDwell - time.Second).UnixNano())
	open, _, _, s := e.exitEvalStarved()
	if !s || open != 1 {
		t.Fatalf("kick dwell>30s → want starved, got open=%d starved=%v", open, s)
	}
	// 取件即清（模拟 positionLoop case 消费）→ 不饥荒
	<-e.kickCh
	e.kickPendingAt.Store(0)
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("kick consumed+pendingAt cleared → must not starve")
	}
	// rendezvous 竞态残余：pendingAt 陈旧非零但 chan 已空 → 不饥荒（len 复核）
	e.kickPendingAt.Store(time.Now().Add(-time.Hour).UnixNano())
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("stale pendingAt with empty chan → must not starve (len guard)")
	}
	e.kickPendingAt.Store(0)
	// carryKick 对称：驻留超界 → 饥荒
	e.KickCarryEval()
	e.carryPendingAt.Store(time.Now().Add(-exitEvalQueueDwell - time.Second).UnixNano())
	if _, _, _, s := e.exitEvalStarved(); !s {
		t.Fatal("carryKick dwell>30s → want starved")
	}
	<-e.carryKick
	e.carryPendingAt.Store(0)
	// 无仓 → 不饥荒
	e.openCount.Store(0)
	e.Kick()
	e.kickPendingAt.Store(time.Now().Add(-exitEvalQueueDwell - time.Second).UnixNano())
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("open=0 → must not starve")
	}
}

// TestD135_KickPendingAccounting pendingAt 记账锚：
// send 成功记驻留起点；merge 丢帧不另记（已被在队 kick 的 pendingAt 代表，不刷新时钟）。
func TestD135_KickPendingAccounting(t *testing.T) {
	e := newD135Engine()
	e.Kick()
	first := e.kickPendingAt.Load()
	if first == 0 {
		t.Fatal("send success must record kickPendingAt")
	}
	e.Kick() // chan 满 → merge 丢帧
	if got := e.kickPendingAt.Load(); got != first {
		t.Fatalf("merged drop must not re-record pendingAt: first=%d got=%d", first, got)
	}
	e.KickCarryEval()
	if e.carryPendingAt.Load() == 0 {
		t.Fatal("carryKick send success must record carryPendingAt")
	}
	e.KickCarryEval() // merge 丢帧
	firstCarry := e.carryPendingAt.Load()
	e.KickCarryEval()
	if got := e.carryPendingAt.Load(); got != firstCarry {
		t.Fatalf("merged carryKick drop must not re-record pendingAt: %d→%d", firstCarry, got)
	}
}

// TestD135_PendingClearedByLoop 消费即清锚：positionLoop case 取件后 pendingAt 归零。
func TestD135_PendingClearedByLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newKickTestEngine(t)
	done := make(chan struct{})
	go func() { e.positionLoop(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // boot 扫描完成

	e.Kick()
	e.KickCarryEval()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) &&
		(e.kickPendingAt.Load() != 0 || e.carryPendingAt.Load() != 0) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.kickPendingAt.Load() != 0 || e.carryPendingAt.Load() != 0 {
		t.Fatalf("loop dequeue must clear pendingAt: kick=%d carry=%d",
			e.kickPendingAt.Load(), e.carryPendingAt.Load())
	}
	cancel()
	<-done
}

// TestD135_WatchdogEdgeTrigger 边沿触发锚：kick 驻留超界 → critical 恰好一次；
// 持续饥荒不重报；恢复后再次饥荒 → 再报一次。
func TestD135_WatchdogEdgeTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origDwell := exitEvalQueueDwell
	exitEvalQueueDwell = 50 * time.Millisecond
	defer func() { exitEvalQueueDwell = origDwell }()

	e := newKickTestEngine(t)
	e.cfg.PositionFallbackInterval = 10 * time.Millisecond // watchdog tick 快跑
	e.openCount.Store(1)

	var alerts int32
	e.AlertSink = func(level, msg string) { atomic.AddInt32(&alerts, 1) }
	go e.exitEvalWatchdog(ctx)

	// kick 入队无人消费 → 驻留超 50ms → critical 一次
	e.Kick()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&alerts) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&alerts) != 1 {
		t.Fatalf("queued kick dwell>dwell → want 1 critical, got %d", atomic.LoadInt32(&alerts))
	}
	// 持续饥荒（kick 仍在队）→ 不再重报（边沿触发）
	time.Sleep(100 * time.Millisecond) // ~10 ticks
	if n := atomic.LoadInt32(&alerts); n != 1 {
		t.Fatalf("sustained starvation must not re-alert, got %d", n)
	}
	// 恢复（消费清零）→ 边沿复位；再次驻留超界 → 第二次告警
	<-e.kickCh
	e.kickPendingAt.Store(0)
	time.Sleep(30 * time.Millisecond) // ≥1 tick 观测到恢复
	e.Kick()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&alerts) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&alerts); n != 2 {
		t.Fatalf("starvation after recovery → want 2nd critical, got %d", n)
	}
}
