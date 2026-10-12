package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/risk/gate"
)

// newD469CEngine 构造事件化测试 Engine：qbus + MeanRevertThresh + kick chans（免 DB——
// 本票测试面全在 dispatch/chan 语义；D-004 P1#4 起 quote 帧送 quoteCh 不再 kickCh）。
func newD469CEngine() *Engine {
	return &Engine{
		cfg: gate.Config{
			Enabled:          true,
			MeanRevertThresh: map[string]float64{"XAU": 1.5, "XAG": 2.0},
		},
		quotes:    bus.New(),
		kickCh:    make(chan struct{}, 1),
		quoteCh:   make(chan bus.Quote, 256),
		carryKick: make(chan struct{}, 1),
		mrKick:    make(chan struct{}, 1),
		factsKick: make(chan struct{}, 1),
	}
}

func recvWithin(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s: no signal within 1s", what)
	}
}

func assertEmpty(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s: unexpected signal", what)
	default:
	}
}

// recvQuoteWithin quoteCh 版 recvWithin（D-004 P1#4：quote 帧走 quoteCh 通道）。
func recvQuoteWithin(t *testing.T, ch chan bus.Quote, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("%s: no quote within 1s", what)
	}
}

// TestD469C_QuoteDispatchMRFilter S1：MeanRevertThresh 覆盖 canonical → mrKick；
// 未覆盖 canonical → 不送 mrKick；两帧均送 e.quoteCh（P1#4 position quote 快路全订）。
func TestD469C_QuoteDispatchMRFilter(t *testing.T) {
	e := newD469CEngine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.quoteKickDispatch(ctx)
	time.Sleep(50 * time.Millisecond) // 订阅就绪（Publish 非阻塞，晚发布会丢）

	// 覆盖 canonical → kickCh + mrKick 双送。
	e.quotes.Publish(bus.Quote{Symbol: "XAU", Venue: "v1", VenueType: "mt5", Bid: 4000, Ask: 4000.1, Time: time.Now()})
	recvQuoteWithin(t, e.quoteCh, "quoteCh tracked")
	recvWithin(t, e.mrKick, "mrKick tracked")

	// 未覆盖 canonical → 只送 quoteCh，mrKick 空。
	e.quotes.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v1", VenueType: "mt5", Bid: 1.08, Ask: 1.0801, Time: time.Now()})
	recvQuoteWithin(t, e.quoteCh, "quoteCh untracked")
	assertEmpty(t, e.mrKick, "mrKick untracked")
}

// TestD469C_MRKickMerge S1：100 帧洪峰 → mrKick buffered(1) 合并为 1；
// Publish 路径不被消费方阻塞（报价热路径铁律）。
func TestD469C_MRKickMerge(t *testing.T) {
	e := newD469CEngine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.quoteKickDispatch(ctx)
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	for i := 0; i < 100; i++ {
		e.quotes.Publish(bus.Quote{Symbol: "XAU", Venue: "v1", VenueType: "mt5", Bid: 4000, Ask: 4000.1, Time: time.Now()})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("100 publishes blocked: took %s", d)
	}
	time.Sleep(100 * time.Millisecond) // dispatch 排空
	got := 0
	for {
		select {
		case <-e.mrKick:
			got++
		default:
			goto done
		}
	}
done:
	if got != 1 {
		t.Fatalf("mrKick merge: got %d pending, want 1", got)
	}
	// quoteCh 收帧：qbus 订阅 chan buffered(1) 非阻塞投递本就丢帧（publish 不被
	// 消费方阻塞=热路径铁律），dispatch 侧不新增丢弃语义——有帧到达即可。
	got = 0
	for {
		select {
		case <-e.quoteCh:
			got++
		default:
			goto done2
		}
	}
done2:
	if got < 1 {
		t.Fatal("quoteCh: no frame delivered")
	}
}

// TestD469C_MRScanMinInterval S1：kick 路径 1s 最小间隔常量钉死（洪峰不放大全扫成本）。
func TestD469C_MRScanMinInterval(t *testing.T) {
	if mrScanMinInterval != time.Second {
		t.Fatalf("mrScanMinInterval=%s, want 1s", mrScanMinInterval)
	}
}

// TestD469C_SwapArbFactsKick S3：factsKick 入队 → swapArbLoop 存活可退；
// 无 kick 时 5min ticker 兜底仍在 select 中（ctx 取消即退=select 正常运转）。
func TestD469C_SwapArbFactsKick(t *testing.T) {
	e := newD469CEngine()
	e.store = testAccumStore(t) // factsKick→scanSwapArb 需真库（空表=零组合快速返回）
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.swapArbLoop(ctx)
		close(done)
	}()

	// factsKick 非阻塞合并（无 DB 扫描验证——scan 路径 D-107 范式同型已覆盖）。
	e.KickSwapArb()
	e.KickSwapArb() // 合并丢弃不阻塞

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("swapArbLoop did not exit after ctx cancel")
	}
}

// TestD469C_KickSwapArbMerge S3：factsKick buffered(1) 合并语义。
func TestD469C_KickSwapArbMerge(t *testing.T) {
	e := newD469CEngine()
	for i := 0; i < 5; i++ {
		e.KickSwapArb()
	}
	recvWithin(t, e.factsKick, "factsKick")
	assertEmpty(t, e.factsKick, "factsKick 2nd")
}

// TestD469C_MeanRevertLoopKickPath S1：meanRevertLoop mrKick case → 扫描节流（1s 内
// 连踢只扫一轮）。可控验证=lastMRScan 只进一次（单 goroutine 写字段，测试在节流窗内读）。
func TestD469C_MeanRevertLoopKickPath(t *testing.T) {
	s := testAccumStore(t)
	if _, err := s.Pool().Exec(context.Background(), `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	e := newD469CEngine()
	e.store = s
	e.cfg.AllowVenues = map[string]bool{"a:mt5": true, "b:mt5": true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.meanRevertLoop(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // loop 进 select

	// 1s 节流窗内连踢三次 → lastMRScan 只应设置一次（值不再前进）。
	e.KickMeanRevert()
	time.Sleep(50 * time.Millisecond) // 第一次扫描完成
	e.KickMeanRevert()
	e.KickMeanRevert()
	time.Sleep(50 * time.Millisecond)
	first := e.lastMRScan
	if first.IsZero() {
		t.Fatal("mrKick did not trigger scan (lastMRScan unset)")
	}
	time.Sleep(100 * time.Millisecond)
	if !e.lastMRScan.Equal(first) {
		t.Fatalf("throttle window: lastMRScan advanced within 1s (%v → %v)", first, e.lastMRScan)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("meanRevertLoop did not exit after ctx cancel")
	}
}
