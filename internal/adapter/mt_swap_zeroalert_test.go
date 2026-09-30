// mt_swap_zeroalert_test.go —— 2026-09-30 PXBT 生产数据缺口热修测试：
// 整轮零产出 critical 告警（6h 节流）+ DeadlineExceeded 符号串行补采。
package adapter

import (
	"context"
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

// TestSwapZeroOutput_Alert 整轮零产出 → critical 一次；6h 内重复轮不重复告警。
func TestSwapZeroOutput_Alert(t *testing.T) {
	q := bus.New()
	rec := &swapAlertRec{}
	// 全部符号 SwapType=0+非零矛盾（server 非白名单 → Uncalibrated skip），emit=0。
	a := &MT5Adapter{venue: "v9", server: "OtherServer-1", AlertSink: rec.sink,
		mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
			"ETHUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 0.1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess}}},
		}}}
	a.SetSymbolMap(map[string]string{"ETHUSD": "ETHUSD"})

	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	n, msg := rec.load()
	if n != 1 {
		t.Fatalf("零产出应告警 1 次，got %d", n)
	}
	if want := "critical|[mt5:v9] swap 采集整轮零产出：skip=1 err=0"; msg[:len(want)] != want {
		t.Errorf("alert msg = %q, want prefix %q", msg, want)
	}
	// 6h 内第二轮（仍零产出）→ 不重复告警。
	_ = captureLog(t, func() {
		_, _ = a.SwapFacts(context.Background(), q)
	})
	if n, _ = rec.load(); n != 1 {
		t.Errorf("6h 节流内第二轮不应再告警，calls=%d", n)
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
