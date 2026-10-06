// mt_swap_zeroalert_test.go —— 2026-09-30 PXBT 生产数据缺口热修测试：
// 整轮零产出 critical 告警（6h 节流）+ DeadlineExceeded 符号串行补采。
// D-086 判定改两轮制：attach 暖窗（ready<5min）零产出只 log 零告警；窗外连续 ≥2 轮才
// critical（单轮瞬时超时降 log）；有产出轮清零计数（暖窗锚/重置锚）。
package adapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
)

// alertRecorder AlertSink 捕获（计数+末条消息）。
type swapAlertRec struct {
	mu    sync.Mutex
	calls int
	last  string
}

func (r *swapAlertRec) sink(level, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.last = level + "|" + msg
}

func (r *swapAlertRec) load() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.last
}

// deadlineOnceClient 指定符号首次 SymbolParams 返 DeadlineExceeded，之后走内嵌 fake。
type deadlineOnceClient struct {
	fakeMT5Client
	mu       sync.Mutex
	failOnce map[string]bool
	calls    map[string]int
}

func (c *deadlineOnceClient) SymbolParams(ctx context.Context, req *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	c.mu.Lock()
	fail := c.failOnce[req.GetSymbol()]
	delete(c.failOnce, req.GetSymbol())
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[req.GetSymbol()]++
	c.mu.Unlock()
	if fail {
		return nil, context.DeadlineExceeded
	}
	return c.fakeMT5Client.SymbolParams(ctx, req, opts...)
}

// zeroOutFixture 恒零产出夹具：全符号 SwapType=0+非零矛盾（server 非白名单 →
// Uncalibrated skip），emit=0。readyAt 零值=未 attach=窗外。
func zeroOutFixture(rec *swapAlertRec) *MT5Adapter {
	a := &MT5Adapter{venue: "v9", server: "OtherServer-1", AlertSink: rec.sink,
		mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
			"ETHUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 0.1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess}}},
		}}}
	a.SetSymbolMap(map[string]string{"ETHUSD": "ETHUSD"})
	return a
}

// TestSwapZeroOutput_Alert D-086 两轮制：窗外首轮零产出零告警；连续第 2 轮恰 1 条
// critical；6h 内第三轮不重复（节流原样保留）。夹具 readyAt 零值=窗外。
func TestSwapZeroOutput_Alert(t *testing.T) {
	q := bus.New()
	rec := &swapAlertRec{}
	a := zeroOutFixture(rec)

	// 连续第 1 轮零产出 → 零告警（瞬时超时观察中，只 log）。
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	if n, _ := rec.load(); n != 0 {
		t.Fatalf("连续第 1 轮零产出不应告警，calls=%d", n)
	}
	// 连续第 2 轮 → 恰 1 条 critical。
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	n, msg := rec.load()
	if n != 1 {
		t.Fatalf("连续第 2 轮零产出应告警 1 次，got %d", n)
	}
	if want := "critical|[mt5:v9] swap 采集整轮零产出：skip=1 err=0"; msg[:len(want)] != want {
		t.Errorf("alert msg = %q, want prefix %q", msg, want)
	}
	// 6h 内第三轮（仍零产出）→ 不重复告警。
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	if n, _ = rec.load(); n != 1 {
		t.Errorf("6h 节流内第三轮不应再告警，calls=%d", n)
	}
}

// TestSwapZeroOutput_AttachWarmup D-086 暖窗锚：attach ready 后 5min 内零产出零告警
// 且不计数；出窗后重新起算——出窗第 1 轮零告警即暖窗轮未计数之证，第 2 轮恰 1 条 critical。
func TestSwapZeroOutput_AttachWarmup(t *testing.T) {
	q := bus.New()
	rec := &swapAlertRec{}
	a := zeroOutFixture(rec)
	a.readyAt = time.Now() // attach ready=now → 暖窗内

	// 暖窗内两轮零产出 → 零告警（不计数不重置）。
	for i := 0; i < 2; i++ {
		_ = captureLog(t, func() {
			_, _ = a.SwapFacts(context.Background(), q)
		})
	}
	if n, _ := rec.load(); n != 0 {
		t.Fatalf("暖窗内零产出不应告警，calls=%d", n)
	}
	// 出窗（ready 已 6min）→ 连续第 1 轮，仍零告警。
	a.readyAt = time.Now().Add(-6 * time.Minute)
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	if n, _ := rec.load(); n != 0 {
		t.Fatalf("出窗后连续第 1 轮不应告警（若暖窗轮被计数此轮即 critical），calls=%d", n)
	}
	// 出窗后连续第 2 轮 → 恰 1 条 critical。
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	n, msg := rec.load()
	if n != 1 {
		t.Fatalf("出窗后连续第 2 轮应恰 1 条 critical，got %d", n)
	}
	if want := "critical|[mt5:v9] swap 采集整轮零产出：skip=1 err=0"; msg[:len(want)] != want {
		t.Errorf("alert msg = %q, want prefix %q", msg, want)
	}
}

// TestSwapZeroOutput_ResetOnEmit D-086 重置锚：零产出轮 → 有产出轮（计数清零）→
// 零产出轮 → 零告警（连续性被打断，重新从第 1 轮起算）。
func TestSwapZeroOutput_ResetOnEmit(t *testing.T) {
	rec := &swapAlertRec{}
	// EURUSD InPoints（有报价才 emit）；ETHUSD SwapNone+非零（恒 Uncalibrated skip）。
	a := &MT5Adapter{venue: "v9", server: "OtherServer-1", AlertSink: rec.sink,
		mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess},
				SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
			"ETHUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 0.1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess}}},
		}}}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "ETHUSD": "ETHUSD"})

	// run 跑一轮：withQuote=false 零产出轮（EURUSD 缺价走 missingPrice、ETHUSD skip）；
	// true 有产出轮（EURUSD InPoints emit）。每轮新 bus（latest 常驻，报价不撤）。
	run := func(withQuote bool) {
		q := bus.New()
		if withQuote {
			q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v9", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
		}
		var fs []fact.Fact
		var err error
		_ = captureLog(t, func() {
			fs, err = a.SwapFacts(context.Background(), q)
		})
		if withQuote {
			if err != nil {
				t.Fatalf("有产出轮不应报错: %v", err)
			}
			if n := countFacts(fs, fact.KindSwapLong); n != 1 {
				t.Fatalf("有产出轮 swap_long facts = %d, want 1", n)
			}
		} else if !errors.Is(err, ErrSwapColdStart) {
			t.Fatalf("零产出轮应报 ErrSwapColdStart（缺价），got %v", err)
		}
	}
	run(false) // 连续第 1 轮零产出 → 只 log
	run(true)  // 有产出轮 → 计数清零，不告警
	if n, _ := rec.load(); n != 0 {
		t.Fatalf("有产出轮不应告警，calls=%d", n)
	}
	run(false) // 清零后再零产出=连续第 1 轮 → 仍零告警
	if n, _ := rec.load(); n != 0 {
		t.Fatalf("有产出轮打断后零产出应为连续第 1 轮零告警，calls=%d", n)
	}
}

// TestSwapZeroOutput_NoAlertWhenEmit emit>0 轮 → 不告警。
func TestSwapZeroOutput_NoAlertWhenEmit(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v9", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	rec := &swapAlertRec{}
	a := &MT5Adapter{venue: "v9", server: "OtherServer-1", AlertSink: rec.sink,
		mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess},
				SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
			"NILUSD": {}, // skip: result nil（有 skip 但 emit>0 → 不告警）
		}}}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "NILUSD": "NILUSD"})

	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	if n, _ := rec.load(); n != 0 {
		t.Errorf("emit>0 轮不应告警，calls=%d", n)
	}
}

// TestSwapFacts_DeadlineRetry DeadlineExceeded 符号补采一次成功 → facts 含该符号。
func TestSwapFacts_DeadlineRetry(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v9", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &deadlineOnceClient{
		fakeMT5Client: fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess},
				SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
		}},
		failOnce: map[string]bool{"EURUSD": true},
	}
	a := &MT5Adapter{venue: "v9", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	var fs []fact.Fact
	var err error
	_ = captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	if n := countFacts(fs, fact.KindSwapLong); n != 1 {
		t.Errorf("补采后 swap_long facts = %d, want 1", n)
	}
	client.mu.Lock()
	calls := client.calls["EURUSD"]
	client.mu.Unlock()
	if calls != 2 {
		t.Errorf("EURUSD SymbolParams 调用 %d 次, want 2（首轮失败+补采成功）", calls)
	}
}
