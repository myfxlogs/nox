// engine_exit_events_test.go —— D-004 P1#4：退出评估分层测试。
// quoteExitHit 纯内存判定矩阵 / quote 快路命中升权威+1s 合流 / carryKick 全扫 /
// 到点边界定时器 / 饥荒看门狗只告警不评估。
package autotrade

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/store/pgstore"
)

// mkOpenEntry 构造内存缓存行（legs 手工给，绕 DB——quoteExitHit 零 DB 语义本身被测）。
func mkOpenEntry(p pgstore.Position) openEntry {
	return openEntry{
		pos:       p,
		canonical: canonicalFromOppID(p.OpportunityID),
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", Symbol: "X", Side: "BUY", Qty: 1},
			{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", Symbol: "X", Side: "SELL", Qty: 1},
		},
	}
}

// TestQuoteExitHit_Matrix quote 快路纯内存判定（quotes.Latest 喂值，无 DB）。
func TestQuoteExitHit_Matrix(t *testing.T) {
	qb := bus.New()
	e := &Engine{cfg: Config{CloseBpsThr: 0.5, BasisStopBps: 4.0}, quotes: qb}
	now := time.Now()

	// 收敛型：卖腿 bid−买腿 ask 剩余 bps ≤ 阈值 → 命中。
	// refPrice=buyAsk=100；sellBid−buyAsk=0 → RemainingBps=0 ≤0.5 → hit。
	entry := mkOpenEntry(pgstore.Position{OpportunityID: "CROSS_VENUE:X:vA:vB:1", Type: "CROSS_VENUE"})
	qb.Publish(bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: now})
	qb.Publish(bus.Quote{Symbol: "X", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.1, Time: now})
	if !e.quoteExitHit(&entry) {
		t.Fatal("CROSS_VENUE 收敛命中应 true")
	}

	// 收敛型未命中：卖腿 bid 高出 → 剩余 bps 大。
	qb.Publish(bus.Quote{Symbol: "X", Venue: "vB", VenueType: "mt5", Bid: 100.5, Ask: 100.6, Time: now})
	if e.quoteExitHit(&entry) {
		t.Fatal("CROSS_VENUE 未收敛应 false")
	}

	// SWAPARB 基差止损：BasisStopBps=4；basis=(buyBid−sellAsk)/ref×10000。
	// 定 entry spread 5bps → PnL_bps=basis−5；basis <1bps → hit。
	entrySwap := mkOpenEntry(pgstore.Position{OpportunityID: "SWAPARB:X:vA:vB:1", Type: "SWAPARB",
		ExpectedEntrySpreadBps: func() *float64 { v := 5.0; return &v }()})
	qb.Publish(bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.99, Ask: 100.0, Time: now})
	qb.Publish(bus.Quote{Symbol: "X", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.0, Time: now})
	if !e.quoteExitHit(&entrySwap) {
		t.Fatal("SWAPARB 基差止损命中应 true")
	}

	// XASSET_CARRY 非 quote 驱动 → 恒 false。
	entryXA := mkOpenEntry(pgstore.Position{OpportunityID: "XASSET_CARRY:X:vA:vB:1", Type: "XASSET_CARRY"})
	if e.quoteExitHit(&entryXA) {
		t.Fatal("XASSET_CARRY 应恒 false（facts 驱动）")
	}
}

// TestQuoteFastPath_Coalesce quote 命中 → 升权威评估（evalHook 计数）；
// 命中态持续 1s 内合流（300ms 内 5 帧只评 1 次）。
func TestQuoteFastPath_Coalesce(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.SetAutotradeState(ctx, StateRunning, "test", nil); err != nil {
		t.Fatalf("state: %v", err)
	}
	// 种 OPEN CROSS_VENUE 仓+双腿。
	pos := &pgstore.Position{OpportunityID: "CROSS_VENUE:X:vA:vB:1", Type: "CROSS_VENUE", Status: "OPEN"}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("pos: %v", err)
	}
	for i, side := range []string{"BUY", "SELL"} {
		venue := "vA:mt5"
		if i == 1 {
			venue = "vB:mt5"
		}
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: id, LegNo: int16(i + 1), Phase: "OPEN", Venue: venue, Symbol: "X",
			Side: side, Qty: 1, Status: "FILLED"}); err != nil {
			t.Fatalf("leg: %v", err)
		}
	}

	e := &Engine{
		cfg:             Config{Enabled: true, CloseBpsThr: 0.5},
		store:           s,
		quotes:          bus.New(),
		kickCh:          make(chan struct{}, 1),
		quoteCh:         make(chan bus.Quote, 256),
		carryKick:       make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
		service: &exitCloseSvc{onClose: func(cctx context.Context, posID int64) error {
			_, err := s.Pool().Exec(cctx, `UPDATE positions SET status='CLOSED' WHERE id=$1`, posID)
			return err
		}}, // 真 dashboard.Service{} 零值 AutoClosePosition 内部 nil → panic
	}
	var evals int32
	e.evalHook = func(int64) { atomic.AddInt32(&evals, 1) }

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { e.positionLoop(lctx); close(done) }()
	time.Sleep(150 * time.Millisecond) // boot 扫+缓存装载

	// boot 扫已对 OPEN 仓各调一次 evaluatePosition（evals=1）。命中升评估要求距
	// 该仓上次权威评估 ≥1s（lastAuth 在 reloadOpen 置 now）——先等出合流窗再发帧。
	time.Sleep(1100 * time.Millisecond)
	// 命中帧：sellBid==buyAsk → 收敛命中（Quote.Venue=裸 venue，VenueKey() 拼成腿侧全键 "vA:mt5"）。
	// 5 帧连发（间隔<1s）→ 权威评估仅 1 次。
	for i := 0; i < 5; i++ {
		// Publish=快路判定取数（quotes.Latest）；quoteCh 直写=喂帧（生产态
		// quoteKickDispatch 转发同帧——本测单起 positionLoop 不起 dispatch）。
		e.quotes.Publish(bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: time.Now()})
		e.quotes.Publish(bus.Quote{Symbol: "X", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.1, Time: time.Now()})
		e.quoteCh <- bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: time.Now()}
		time.Sleep(60 * time.Millisecond) // 5 帧 ≈300ms 内
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&evals) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&evals) != 2 {
		t.Fatalf("命中合流：evals=%d, want 2（boot 1 + 命中升权威 1，1s 内持续命中只升一次）", atomic.LoadInt32(&evals))
	}

	// 非命中 canonical 帧 → 不触发任何权威评估。
	e.quotes.Publish(bus.Quote{Symbol: "OTHER", Venue: "vA", VenueType: "mt5", Bid: 1, Ask: 1, Time: time.Now()})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&evals) != 2 {
		t.Fatalf("非本仓 canonical 帧不应升评估: evals=%d", atomic.LoadInt32(&evals))
	}
}

// TestCarryKick_FullScan carryKick → 权威全扫 1 次（scanHook 观测）。
func TestCarryKick_FullScan(t *testing.T) {
	e := newKickTestEngine(t)
	var scans int32
	e.scanHook = func() { atomic.AddInt32(&scans, 1) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.positionLoop(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	base := atomic.LoadInt32(&scans)

	e.KickCarryEval()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&scans) < base+1 {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&scans) != base+1 {
		t.Fatalf("carryKick 应触发一次全扫: scans=%d base=%d", scans, base)
	}
}

// TestExitBoundaryTimer PERP_FUNDING 持有下限到点 → 全扫触发；空仓 → 定时器不触发。
func TestExitBoundaryTimer(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.SetAutotradeState(ctx, StateRunning, "test", nil); err != nil {
		t.Fatalf("state: %v", err)
	}
	e := &Engine{
		cfg:             Config{Enabled: true, PerpFundMinHold: time.Second},
		store:           s,
		service:         &dashboard.Service{},
		quotes:          bus.New(),
		kickCh:          make(chan struct{}, 1),
		quoteCh:         make(chan bus.Quote, 256),
		carryKick:       make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
	}
	var scans int32
	e.scanHook = func() { atomic.AddInt32(&scans, 1) }

	// 空仓先行：loop 起 1.5s 内除 boot 外不应有 scan（无边界）。
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.positionLoop(lctx); close(done) }()
	time.Sleep(1500 * time.Millisecond)
	if got := atomic.LoadInt32(&scans); got != 1 {
		t.Fatalf("空仓不应有边界触发: scans=%d want 1(boot)", got)
	}
	cancel()
	<-done

	// 种 PERP_FUNDING 仓 OpenedAt=now-800ms（minHold=1s → 边界=now+200ms，+1s 到点≈1.2s）。
	opened := time.Now().Add(-800 * time.Millisecond)
	pos := &pgstore.Position{OpportunityID: "PERP_FUNDING:X:vA:vB:1", Type: "PERP_FUNDING", Status: "OPEN", OpenedAt: &opened}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("pos: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:perp", Symbol: "X", Side: "BUY", Qty: 1, Status: "FILLED"}); err != nil {
		t.Fatalf("leg: %v", err)
	}
	lctx2, cancel2 := context.WithCancel(ctx)
	done2 := make(chan struct{})
	go func() { e.positionLoop(lctx2); close(done2) }()
	base := atomic.LoadInt32(&scans) // 含第二次 boot 扫前的快照可能含+1——以增量判
	deadline := time.Now().Add(2 * time.Second)
	want := base + 1 // 边界到点触发（boot 扫描与 base 读取有竞态，只断言增量 ≥1 且 ≤2）
	for time.Now().Before(deadline) && atomic.LoadInt32(&scans) < want {
		time.Sleep(20 * time.Millisecond)
	}
	got := atomic.LoadInt32(&scans) - base
	if got < 1 {
		t.Fatalf("PERP_FUNDING 持有下限到点应触发全扫: delta=%d", got)
	}
	cancel2()
	<-done2
}

// TestWatchdog_AlertOnly 饥荒看门狗：有仓 + 评估久未跑 + 报价在流 → critical 告警；
// scan 计数不增（看门狗不评估）。
func TestWatchdog_AlertOnly(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.SetAutotradeState(ctx, StateRunning, "test", nil); err != nil {
		t.Fatalf("state: %v", err)
	}
	pos := &pgstore.Position{OpportunityID: "CROSS_VENUE:X:vA:vB:1", Type: "CROSS_VENUE", Status: "OPEN"}
	if _, err := s.CreatePosition(ctx, pos); err != nil {
		t.Fatalf("pos: %v", err)
	}
	var alerts int32
	var msgsMu sync.Mutex
	var msgs []string
	e := &Engine{
		cfg:             Config{Enabled: true, PositionFallbackInterval: 60 * time.Millisecond},
		store:           s,
		service:         &dashboard.Service{},
		quotes:          bus.New(),
		kickCh:          make(chan struct{}, 1),
		quoteCh:         make(chan bus.Quote, 256),
		carryKick:       make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
		AlertSink: func(level, msg string) {
			atomic.AddInt32(&alerts, 1)
			msgsMu.Lock()
			msgs = append(msgs, level+":"+msg)
			msgsMu.Unlock()
		},
	}
	var scans int32
	e.scanHook = func() { atomic.AddInt32(&scans, 1) }

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { e.positionLoop(lctx); close(done) }()
	time.Sleep(100 * time.Millisecond) // boot 扫描+缓存装载

	// lastEval 已在 boot 设；watchdog 周期 60ms → 有仓+报价在流+评估>60ms → 告警。
	// 持续喂 quote 帧（lastQuoteSeen 新鲜），但报价不命中（收敛 bid 大 → quoteExitHit false）。
	for i := 0; i < 5; i++ {
		e.quoteCh <- bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 200, Ask: 200.1, Time: time.Now()}
		time.Sleep(30 * time.Millisecond)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && atomic.LoadInt32(&alerts) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&alerts) == 0 {
		t.Fatal("看门狗应发 critical 告警")
	}
	msgsMu.Lock()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "critical") && strings.Contains(m, "看门狗") {
			found = true
		}
	}
	msgsMu.Unlock()
	if !found {
		t.Fatalf("告警应含 critical+看门狗文案: %v", msgs)
	}
	if got := atomic.LoadInt32(&scans); got != 1 {
		t.Fatalf("看门狗不得触发评估: scans=%d want 1(boot)", got)
	}
}

// exitCloseSvc D-004 P1#4 返修用 fake：只覆写 AutoClosePosition（其余方法经
// 内嵌 nil 接口提升，本测试路径不触达）。
type exitCloseSvc struct {
	engineService
	onClose func(ctx context.Context, posID int64) error
}

func (f *exitCloseSvc) AutoClosePosition(ctx context.Context, posID int64) error {
	return f.onClose(ctx, posID)
}

// TestQuoteFastPath_ReloadBreak D-004 P1#4 返修：同帧两命中仓——第一仓平仓后
// reload 切片变短，旧 range 索引继续走会 panic/评错仓；修后 break，第二仓本帧
// 不评估（下一帧帧流再评）。断言不 panic + evals 只 +1 + 第二仓仍 OPEN。
func TestQuoteFastPath_ReloadBreak(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, autotrade_state, ticks CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.SetAutotradeState(ctx, StateRunning, "test", nil); err != nil {
		t.Fatalf("state: %v", err)
	}
	for j := 0; j < 2; j++ {
		pos := &pgstore.Position{OpportunityID: fmt.Sprintf("CROSS_VENUE:X:vA:vB:%d", j), Type: "CROSS_VENUE", Status: "OPEN"}
		id, err := s.CreatePosition(ctx, pos)
		if err != nil {
			t.Fatalf("pos%d: %v", j, err)
		}
		for i, side := range []string{"BUY", "SELL"} {
			venue := "vA:mt5"
			if i == 1 {
				venue = "vB:mt5"
			}
			if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
				PositionID: id, LegNo: int16(i + 1), Phase: "OPEN", Venue: venue, Symbol: "X",
				Side: side, Qty: 1, Status: "FILLED"}); err != nil {
				t.Fatalf("leg: %v", err)
			}
		}
	}
	// allLegsActionable 需要窗口内 bid 变化（D-495 判据）——两腿裸 venue 各插两笔。
	now := time.Now()
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now.Add(-time.Second), Venue: "vA", Symbol: "X", Bid: 99.9, Ask: 100.0, VenueType: "mt5"},
		{Ts: now, Venue: "vA", Symbol: "X", Bid: 99.95, Ask: 100.0, VenueType: "mt5"},
		{Ts: now.Add(-time.Second), Venue: "vB", Symbol: "X", Bid: 100.0, Ask: 100.1, VenueType: "mt5"},
		{Ts: now, Venue: "vB", Symbol: "X", Bid: 100.05, Ask: 100.1, VenueType: "mt5"},
	}); err != nil {
		t.Fatalf("ticks: %v", err)
	}

	var evals int32
	e := &Engine{
		cfg:             Config{Enabled: true, CloseBpsThr: 0.5},
		store:           s,
		quotes:          bus.New(),
		kickCh:          make(chan struct{}, 1),
		quoteCh:         make(chan bus.Quote, 256),
		carryKick:       make(chan struct{}, 1),
		fundingNeedSent: make(map[string]string),
		service: &exitCloseSvc{onClose: func(cctx context.Context, posID int64) error {
			_, err := s.Pool().Exec(cctx, `UPDATE positions SET status='CLOSED' WHERE id=$1`, posID)
			return err
		}},
	}
	e.evalHook = func(int64) { atomic.AddInt32(&evals, 1) }

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { e.positionLoop(lctx); close(done) }()
	time.Sleep(150 * time.Millisecond) // boot 扫：无报价 → 不平仓，evals=2

	// 等出 1s 命中合流窗（lastAuth=reload 时刻），再发收敛报价 + 触发帧。
	time.Sleep(1100 * time.Millisecond)
	e.quotes.Publish(bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: time.Now()})
	e.quotes.Publish(bus.Quote{Symbol: "X", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.1, Time: time.Now()})
	base := atomic.LoadInt32(&evals)
	e.quoteCh <- bus.Quote{Symbol: "X", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: time.Now()}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&evals) < base+1 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // 给潜在的越界/多评窗口——不应再有 eval
	if got := atomic.LoadInt32(&evals); got != base+1 {
		t.Fatalf("同帧第二仓不应评估（break 语义）: evals=%d base=%d", got, base)
	}
	var openN, closedN int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*) FILTER (WHERE status='OPEN'), COUNT(*) FILTER (WHERE status='CLOSED') FROM positions`).Scan(&openN, &closedN); err != nil {
		t.Fatalf("count: %v", err)
	}
	if openN != 1 || closedN != 1 {
		t.Fatalf("应只平一仓（另一仓待下帧）: open=%d closed=%d", openN, closedN)
	}
	cancel()
	<-done
}

// TestReloadOpen_StaleCacheOnError D-004 P1#4 返修：ListPositions 出错 → 返回
// prev（陈旧缓存优于失明——返 nil 会让快路全盲且看门狗 len(open)>0 静默）。
func TestReloadOpen_StaleCacheOnError(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := &pgstore.Position{OpportunityID: "CROSS_VENUE:X:vA:vB:1", Type: "CROSS_VENUE", Status: "OPEN"}
	if _, err := s.CreatePosition(ctx, pos); err != nil {
		t.Fatalf("pos: %v", err)
	}
	e := &Engine{store: s}
	first := e.reloadOpen(ctx, nil)
	if len(first) != 1 {
		t.Fatalf("首载应有 1 仓, got %d", len(first))
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel() // 已取消 ctx → ListPositions 必错
	got := e.reloadOpen(cctx, first)
	if len(got) != 1 || got[0].pos.ID != first[0].pos.ID {
		t.Fatalf("出错应保留陈旧缓存: %+v", got)
	}
}
