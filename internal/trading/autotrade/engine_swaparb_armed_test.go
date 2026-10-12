// engine_swaparb_armed_test.go —— D-004 P1#5：swapArb armed+quote 触发分层测试。
// swapQuoteGates 纯函数矩阵 / armed→quote 触发含 tried 锁与重建 /
// dispatch 武装 canonical 过滤 / KickSwapArb 合流全扫 / 两处看门狗只告警不扫。
package autotrade

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// TestSwapQuoteGates_Matrix quote 依赖门禁纯函数表驱动（盲腿/陈旧/腿宽/基差/价差上下限/全过）。
func TestSwapQuoteGates_Matrix(t *testing.T) {
	now := time.Now()
	cfg := gate.Config{
		FreshnessTTL:           time.Minute,
		SwapArbMaxLegSpreadBps: 50,
		EntryBasisMaxBps:       100,
		EntrySpreadMinBps:      -3,
		EntrySpreadMaxBps:      500,
	}
	good := func() (bus.Quote, bus.Quote) {
		return bus.Quote{Symbol: "X", Bid: 99.9, Ask: 100.0, Time: now},
			bus.Quote{Symbol: "X", Bid: 100.0, Ask: 100.1, Time: now}
	}
	ql, qs := good()
	cases := []struct {
		name       string
		ql, qs     bus.Quote
		wantReject bool
	}{
		{"all pass", ql, qs, false},
		{"stale long", bus.Quote{Symbol: "X", Bid: 99.9, Ask: 100.0, Time: now.Add(-2 * time.Minute)}, qs, true},
		{"leg spread wide", bus.Quote{Symbol: "X", Bid: 90, Ask: 110, Time: now}, qs, true},
		{"basis diverge", ql, bus.Quote{Symbol: "X", Bid: 120, Ask: 120.1, Time: now}, true},
		{"ask<=0 blind", bus.Quote{Symbol: "X", Bid: 99, Ask: 0, Time: now}, qs, true},
		{"spread below min", ql, bus.Quote{Symbol: "X", Bid: 99.0, Ask: 99.1, Time: now}, true},
		{"spread too good", ql, bus.Quote{Symbol: "X", Bid: 110, Ask: 110.1, Time: now}, true},
	}
	for _, tc := range cases {
		_, reject, _ := swapQuoteGates(tc.ql, tc.qs, now, cfg)
		if reject != tc.wantReject {
			t.Errorf("%s: reject=%v want %v", tc.name, reject, tc.wantReject)
		}
	}
	// spreadBps 返回值（全过用例应回 0bps 级数）。
	if bps, rej, _ := swapQuoteGates(ql, qs, now, cfg); rej || bps != 0 {
		t.Errorf("all-pass spreadBps=%.2f reject=%v, want 0/false", bps, rej)
	}
}

// armedFakeSvc 只覆写 SwapArbNotional（计权威评估次数；返回 !ok 提前短路——
// 不触达 gate.Submit）。其余 engineService 方法经内嵌 nil 提升，本路径不触达。
type armedFakeSvc struct {
	engineService
	notionalCalls *int32
}

func (f *armedFakeSvc) SwapArbNotional(_, _, _ string) (float64, bool, string) {
	atomic.AddInt32(f.notionalCalls, 1)
	return 0, false, "test" // 勿含 "sizing"——会触发 maybePublishFundingNeed 调嵌口方法
}

// newSwapArbArmedEngine D-004 P1#5 测试引擎：facts 双 venue 种子（d388Seed）→
// boot 全扫武装 AUDJPY 组合；service fake 计数 SwapArbNotional=权威尝试锚点。
func newSwapArbArmedEngine(t *testing.T) (*Engine, *pgstore.Store, *int32) {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	d388Seed(t, s, "connected", "connected")
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, autotrade_logs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var notionalCalls int32
	e := &Engine{
		cfg: gate.Config{
			Enabled:              true,
			MinNetSwapBps:        0,
			SwapFactMaxAge:       2 * time.Hour,
			MaxPositions:         10,
			EntrySpreadMinBps:    -3,
			EntrySpreadMaxBps:    500,
			EntryBasisMaxBps:     1000,
			FreshnessTTL:         time.Minute,
			MeanRevertThresh:     nil,
			PerpFundExitDebounce: 2,
		},
		store:       s,
		quotes:      bus.New(),
		service:     &armedFakeSvc{notionalCalls: &notionalCalls},
		factsKick:   make(chan struct{}, 1),
		swapQuoteCh: make(chan bus.Quote, 256),
	}
	return e, s, &notionalCalls
}

// TestSwapArb_ArmedQuoteTrigger 武装→quote 触发：不合格帧不升权威；合格帧一次；
// tried 锁防重复；factsKick 全扫重建 armed 后再帧可再试。
func TestSwapArb_ArmedQuoteTrigger(t *testing.T) {
	e, _, notionalCalls := newSwapArbArmedEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.swapArbLoop(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond) // boot 全扫+武装

	if !e.swapArmedCanonical("AUDJPY") {
		t.Fatal("boot 全扫后 AUDJPY 应在武装 canonical 集")
	}
	if got := atomic.LoadInt32(notionalCalls); got != 0 {
		t.Fatalf("boot 扫盲腿（无报价）不应到 sizing: calls=%d", got)
	}

	// 价差不合格帧（basis 发散 > EntryBasisMaxBps）：纯内存拒，不升权威。
	e.quotes.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 99.9, Ask: 100.0, Time: time.Now()})
	e.quotes.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 140.0, Ask: 140.1, Time: time.Now()})
	e.swapQuoteCh <- bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5"}
	time.Sleep(150 * time.Millisecond)
	if got := atomic.LoadInt32(notionalCalls); got != 0 {
		t.Fatalf("价差不合格帧不应升权威: calls=%d", got)
	}

	// 合格帧（basis≈0, spreadBps=0 ∈ [min,max]）→ 一次权威尝试。
	e.quotes.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 100.0, Ask: 100.1, Time: time.Now()})
	e.swapQuoteCh <- bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(notionalCalls) < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(notionalCalls); got != 1 {
		t.Fatalf("合格帧应升一次权威评估: calls=%d", got)
	}

	// tried 锁：再发 10 帧合格 → 仍 1（每武装至多一次，零轮询）。
	for i := 0; i < 10; i++ {
		e.swapQuoteCh <- bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5"}
	}
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(notionalCalls); got != 1 {
		t.Fatalf("tried 锁后不应再升权威: calls=%d", got)
	}

	// factsKick → 全扫重建 armed（scan 内 trySwapArb 报价齐 → calls=2），
	// tried 复位 → 合格帧再升一次（calls=3）。
	e.KickSwapArb()
	deadline = time.Now().Add(3 * time.Second) // boot 后 <1s → 合流定时器补跑
	for time.Now().Before(deadline) && atomic.LoadInt32(notionalCalls) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(notionalCalls); got != 2 {
		t.Fatalf("factsKick 全扫应再试一次（报价齐）: calls=%d", got)
	}
	e.swapQuoteCh <- bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5"}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(notionalCalls) < 3 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(notionalCalls); got != 3 {
		t.Fatalf("重建后合格帧应再升权威: calls=%d", got)
	}
	cancel()
	<-done
}

// TestSwapQuoteDispatch_ArmedOnly quoteKickDispatch：非武装 canonical 帧不进
// swapQuoteCh；武装 canonical 帧进。
func TestSwapQuoteDispatch_ArmedOnly(t *testing.T) {
	e := &Engine{
		quotes:      bus.New(),
		quoteCh:     make(chan bus.Quote, 256),
		swapQuoteCh: make(chan bus.Quote, 256),
		mrKick:      make(chan struct{}, 1),
		cfg:         gate.Config{},
	}
	armed := map[string]struct{}{"AUDJPY": {}}
	e.swapArmedCanon.Store(&armed)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.quoteKickDispatch(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond) // dispatch 订阅就绪

	e.quotes.Publish(bus.Quote{Symbol: "EURUSD", Venue: "vA", VenueType: "mt5", Bid: 1, Ask: 1.1, Time: time.Now()})
	e.quotes.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100, Ask: 100.1, Time: time.Now()})
	select {
	case q := <-e.swapQuoteCh:
		if q.Symbol != "AUDJPY" {
			t.Fatalf("swapQuoteCh 收到非武装帧: %s", q.Symbol)
		}
	case <-time.After(time.Second):
		t.Fatal("武装 canonical 帧应进 swapQuoteCh")
	}
	select {
	case q := <-e.swapQuoteCh:
		t.Fatalf("不应再收帧: %s", q.Symbol)
	default:
	}
	cancel()
	<-done
}

// TestSwapArb_FactsKickCoalesce factsKick 1s 合流不丢：boot 后 <1s 的 kick
// 由定时器补跑——两次 kick 得两次全扫。
func TestSwapArb_FactsKickCoalesce(t *testing.T) {
	e, _, _ := newSwapArbArmedEngine(t)
	var scans int32
	e.swapScanHook = func() { atomic.AddInt32(&scans, 1) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.swapArbLoop(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond)
	base := atomic.LoadInt32(&scans) // boot=1

	e.KickSwapArb()
	time.Sleep(100 * time.Millisecond)
	e.KickSwapArb() // 窗口内第二次 → pending 补跑（不丢）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&scans) < base+1 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&scans); got < base+1 {
		t.Fatalf("kick 合流补跑未发生: scans=%d base=%d", got, base)
	}
	cancel()
	<-done
}

// TestSwapArb_Watchdog facts 饥荒看门狗：lastFactsKick 超期 → critical 告警且 scan 不增。
func TestSwapArb_Watchdog(t *testing.T) {
	old := swapFactsWatchdog
	swapFactsWatchdog = 60 * time.Millisecond
	defer func() { swapFactsWatchdog = old }()

	e, _, _ := newSwapArbArmedEngine(t)
	var scans, alerts int32
	var mu sync.Mutex
	var msgs []string
	e.swapScanHook = func() { atomic.AddInt32(&scans, 1) }
	e.AlertSink = func(level, msg string) {
		atomic.AddInt32(&alerts, 1)
		mu.Lock()
		msgs = append(msgs, level+":"+msg)
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { e.swapArbLoop(ctx); close(done) }()
	time.Sleep(150 * time.Millisecond) // boot 扫 + 等待 >60ms 无 factsKick
	base := atomic.LoadInt32(&scans)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&alerts) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	found := false
	for _, m := range msgs {
		if len(m) > 8 && m[:8] == "critical" {
			found = true
		}
	}
	mu.Unlock()
	if !found {
		t.Fatalf("facts 饥荒应发 critical 告警: %v", msgs)
	}
	if got := atomic.LoadInt32(&scans); got != base {
		t.Fatalf("看门狗不得触发扫描: scans=%d base=%d", got, base)
	}
	cancel()
	<-done
}

// TestMeanRevert_Watchdog mr kick 在流而扫描久未跑 → WATCHDOG critical；
// 无 kick（报价不在流）→ 不告警。
func TestMeanRevert_Watchdog(t *testing.T) {
	old := mrWatchdogInterval
	mrWatchdogInterval = 60 * time.Millisecond
	defer func() { mrWatchdogInterval = old }()

	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mkEngine := func() (*Engine, *int32, *int32) {
		var scans, alerts int32
		e := &Engine{
			cfg: gate.Config{
				Enabled:          true,
				MeanRevertThresh: map[string]float64{"X": 5},
				AllowVenues:      map[string]bool{"vA:mt5": true, "vB:mt5": true},
				MaxPositions:     10,
			},
			store:      s,
			quotes:     bus.New(),
			mrKick:     make(chan struct{}, 1),
			mrScanHook: func() { atomic.AddInt32(&scans, 1) },
			AlertSink:  func(level, msg string) { atomic.AddInt32(&alerts, 1) },
		}
		return e, &scans, &alerts
	}

	// 情形 1：kick 在流（1s 节流压住扫描）→ 看门狗告警，scan 不增。
	e, scans, alerts := mkEngine()
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.meanRevertLoop(lctx); close(done) }()
	e.KickMeanRevert() // 首 kick → 扫一次（lastMRScan=now）
	time.Sleep(30 * time.Millisecond)
	for i := 0; i < 8; i++ { // 持续 kick（均被 1s 节流吞）→ lastMRQuoteSeen 新鲜
		e.KickMeanRevert()
		time.Sleep(30 * time.Millisecond)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(alerts) == 0 {
		e.KickMeanRevert()
		time.Sleep(40 * time.Millisecond)
	}
	if atomic.LoadInt32(alerts) == 0 {
		t.Fatal("kick 在流而扫描饥荒应发 critical 告警")
	}
	if got := atomic.LoadInt32(scans); got != 1 {
		t.Fatalf("看门狗/节流期不应多扫: scans=%d want 1", got)
	}
	cancel()
	<-done

	// 情形 2：无 kick → 不告警（lastMRQuoteSeen 零值守卫）。
	e2, _, alerts2 := mkEngine()
	lctx2, cancel2 := context.WithCancel(ctx)
	done2 := make(chan struct{})
	go func() { e2.meanRevertLoop(lctx2); close(done2) }()
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(alerts2) != 0 {
		t.Fatal("报价不在流不应告警")
	}
	cancel2()
	<-done2
}
