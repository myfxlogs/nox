package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
)

// newKickTestEngine 构造带 DB store 的 Engine（空表，scanAndClose/stateMachineTick 快速返回）。
// D-107 P4 单测共用：Kick 合并 / scanIfDue 节流 / fallback 触发。
func newKickTestEngine(t *testing.T, scanThrottle time.Duration) *Engine {
	t.Helper()
	s := testAccumStore(t)
	// 清空表防串扰。
	if _, err := s.Pool().Exec(context.Background(), `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 初始化 autotrade_state（stateMachineTick 依赖 GetAutotradeState）。
	if err := s.SetAutotradeState(context.Background(), StateRunning, "test", nil); err != nil {
		t.Fatalf("set autotrade state: %v", err)
	}
	return &Engine{
		cfg: Config{
			Enabled:      true,
			ScanThrottle: scanThrottle,
		},
		store:           s,
		service:         &dashboard.Service{},
		kickCh:          make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
	}
}

// TestEngine_KickMerge D-107 P4：连续多 kick 只缓冲 1 个（chan 容量 1 合并语义）。
// Kick 3 次 → kickCh 只含 1 个待处理 → 接收后为空。
func TestEngine_KickMerge(t *testing.T) {
	e := newKickTestEngine(t, 50*time.Millisecond)
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

// TestEngine_ScanIfDue_ThrottleWindow D-107 P4：节流窗口内 kick 不扫、超窗扫。
// ScanThrottle=50ms：窗口内 scanIfDue 跳过（lastScan 刚设）、超窗后 scanIfDue 执行。
// 验证：scanIfDue 执行后 lastScan 更新（time.Since(lastScan) 重置）。
func TestEngine_ScanIfDue_ThrottleWindow(t *testing.T) {
	ctx := context.Background()
	e := newKickTestEngine(t, 50*time.Millisecond)

	// 模拟 positionLoop 启动即扫（设 lastScan 基准）。
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	e.lastScan = time.Now()

	// 1. 窗口内 scanIfDue → 节流跳过（lastScan 不更新）。
	lastScanBefore := e.lastScan
	e.scanIfDue(ctx)
	if e.lastScan != lastScanBefore {
		t.Fatalf("within throttle window: lastScan should not update, got changed")
	}

	// 2. 超窗后 scanIfDue → 执行扫描 + 更新 lastScan。
	time.Sleep(60 * time.Millisecond) // 超过 50ms 窗口
	e.scanIfDue(ctx)
	if !e.lastScan.After(lastScanBefore) {
		t.Fatalf("after throttle window: lastScan should update, got %v <= %v", e.lastScan, lastScanBefore)
	}
}

// TestEngine_FallbackTrigger D-107 P4：positionFallbackInterval 覆写 10ms → fallback ticker 触发直扫。
// 启动 positionLoop → fallback 10ms 后触发 scanAndClose+stateMachineTick。
// 验证：positionLoop 不阻塞、fallback ticker 正常触发（间接验证：positionLoop 存活 + ctx 取消后退出）。
// lastScan 无锁仅 positionLoop goroutine 读写——测试不跨 goroutine 读 lastScan（race-free）。
func TestEngine_FallbackTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// 覆写 fallback 间隔到 10ms（测试快跑）。
	origFallback := positionFallbackInterval
	positionFallbackInterval = 10 * time.Millisecond
	defer func() { positionFallbackInterval = origFallback }()

	e := newKickTestEngine(t, 30*time.Second) // ScanThrottle 大值防 kick 干扰

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
	e := newKickTestEngine(t, 50*time.Millisecond)
	orderTopic := bus.NewTopic[bus.OrderEvent]()

	// 模拟 setup.go 装配：orderTopic 订阅 → atEngine.Kick。
	// readyCh 确保订阅就绪后再 Publish（防 goroutine 启动竞态）。
	readyCh := make(chan struct{})
	go func() {
		evCh, evCancel := orderTopic.Subscribe()
		defer evCancel()
		close(readyCh)
		for range evCh {
			e.Kick()
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
