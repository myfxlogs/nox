package autotrade

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
)

// TestEngine_KickMerge D-107 P4：连续多 kick 只缓冲 1 个（chan 容量 1 合并语义）。
// Kick 3 次 → kickCh 只含 1 个待处理 → 接收后为空。
func TestEngine_KickMerge(t *testing.T) {
	e := newKickTestEngine(t)
	e.Kick()
	e.Kick()
	e.Kick() // 3 次 kick，chan 容量 1 → 只缓冲 1 个

	// 接收第 1 个（应有且仅有 1 个）。
	select {
	case <-e.kickCh:
	default:
		t.Fatal("expected 1 pending kick in chan, got 0")
	}
	// 第 2、3 个应被合并丢弃——chan 空。
	select {
	case <-e.kickCh:
		t.Fatal("expected kick merge (chan empty after 1 recv), got 2nd")
	default:
		// 正确：合并丢弃
	}
}

// TestEngine_KickCoalesce D-004 P1#4：订单 kick 窗口内不丢事件——两次 Kick 间隔
// <kickCoalesce(1s) → 第二次由合流定时器补跑。scanHook 计数观测（scanAndClose 入口）。
func TestEngine_KickCoalesce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newKickTestEngine(t)
	var scans int32
	e.scanHook = func() { atomic.AddInt32(&scans, 1) }

	done := make(chan struct{})
	go func() { e.positionLoop(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // boot 扫描完成
	base := atomic.LoadInt32(&scans)  // =1

	// kick1：距 boot 扫描 <1s → 置 pendingKick；kick2：合并进 pending（不丢）。
	e.Kick()
	time.Sleep(100 * time.Millisecond)
	e.Kick()

	// 1s 合流到点 → 补跑一次全扫。
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(&scans) < base+1 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&scans) < base+1 {
		t.Fatalf("kick 合流补跑未发生: scans=%d base=%d", atomic.LoadInt32(&scans), base)
	}

	// 超窗 kick → 立即全扫（不丢也不拖）。
	time.Sleep(1100 * time.Millisecond)
	e.Kick()
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(&scans) < base+2 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&scans) < base+2 {
		t.Fatalf("超窗 kick 应立即全扫: scans=%d", atomic.LoadInt32(&scans))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("positionLoop did not exit after ctx cancel")
	}
}

// TestEngine_WatchdogTickerAlive D-004 P1#4：watchdog ticker（原 fallback）转为只告警
// 不评估——空仓下 tick 不触发 scan；positionLoop 仍随 ctx 取消退出（select 正常运转）。
func TestEngine_FallbackTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// 覆写 fallback 间隔到 10ms（测试快跑）。
	origFallback := positionFallbackInterval
	positionFallbackInterval = 10 * time.Millisecond
	defer func() { positionFallbackInterval = origFallback }()

	e := newKickTestEngine(t)

	// positionLoop 启动即扫 + fallback 10ms ticker。
	done := make(chan struct{})
	go func() {
		e.positionLoop(ctx)
		close(done)
	}()

	// 等 fallback ticker 触发几次（50ms = ~5 次 fallback）。
	time.Sleep(50 * time.Millisecond)

	// 取消 ctx → positionLoop 应退出（证明 fallback ticker select 正常运转未阻塞）。
	cancel()
	select {
	case <-done:
		// 正确：positionLoop 收到 ctx.Done 退出 = select 三支正常运转
	case <-time.After(time.Second):
		t.Fatal("positionLoop did not exit after ctx cancel (fallback ticker blocked?)")
	}
}

// TestEngine_OrderTopicToKickWiring D-107 P4：orderTopic Publish → 订阅 goroutine → atEngine.Kick 接线验证。
// 模拟 setup.go 的订阅 goroutine：orderTopic.Subscribe → for range → atEngine.Kick()。
func TestEngine_OrderTopicToKickWiring(t *testing.T) {
	e := newKickTestEngine(t)
	orderTopic := bus.NewTopic[bus.OrderEvent]()

	// 模拟 setup.go 装配：orderTopic 订阅 → atEngine.Kick。
	// readyCh 确保订阅就绪后再 Publish（防 goroutine 启动竞态）。
	// 测试内订阅循环必须 select ctx.Done（D-207：防泄漏 goroutine 持池连接→死锁/EBADF）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan struct{})
	go func() {
		evCh, evCancel := orderTopic.Subscribe()
		defer evCancel()
		close(readyCh)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-evCh:
				if !ok {
					return
				}
				e.Kick()
			}
		}
	}()
	<-readyCh // 等订阅就绪

	// Publish 一个 OrderEvent → 经订阅 goroutine → Kick → kickCh 收到。
	orderTopic.Publish(bus.OrderEvent{Venue: "52993526:mt5", Ticket: 1})

	// 等 Kick 到达 kickCh（订阅 goroutine 异步，给点时间）。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-e.kickCh:
			return // 收到 = 接线正确
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("orderTopic→Kick wiring: kickCh did not receive after publish")
}
