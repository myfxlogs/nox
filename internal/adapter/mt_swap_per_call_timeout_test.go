// mt_swap_per_call_timeout_test.go —— S5（2026-08-24）：SwapFacts per-call 3s 超时。
// 模拟慢符号（4s 延迟 mock）→ 断言该符号超时进 errs、其余符号照常输出、整体不超时、facts>0 时不返回 err。
package adapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
)

// slowFakeMT5Client 模拟 PXBT 慢 SymbolParams：指定符号阻塞 delay 后返回，其余即时。
type slowFakeMT5Client struct {
	mt5pb.MT5Client
	mu       sync.Mutex
	params   map[string]*mt5pb.SymbolParamsReply
	slowSym  string
	delay    time.Duration
	gotCalls map[string]int
	leverage float64 // S6 返修：AccountSummary.Leverage（0=未设→fetchLeverage 守卫不发）
}

func (f *slowFakeMT5Client) SymbolParams(ctx context.Context, req *mt5pb.SymbolParamsRequest, _ ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	f.mu.Lock()
	f.gotCalls[req.GetSymbol()]++
	f.mu.Unlock()
	if req.GetSymbol() == f.slowSym {
		// 模拟服务器慢响应：阻塞 delay，但尊重 ctx（被 per-call 超时取消时立即返回）。
		select {
		case <-time.After(f.delay):
			if r, ok := f.params[req.GetSymbol()]; ok {
				return r, nil
			}
			return nil, errors.New("not found")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r, ok := f.params[req.GetSymbol()]; ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

// AccountSummary S6 返修：返回预设 leverage（0→fetchLeverage 守卫不发 margin fact，
// 不影响慢符号超时测试主线）。
func (f *slowFakeMT5Client) AccountSummary(_ context.Context, _ *mt5pb.AccountSummaryRequest, _ ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	return &mt5pb.AccountSummaryReply{Result: &mt5pb.AccountSummary{Leverage: f.leverage}}, nil
}

// TestSwapFacts_PerCallTimeout 慢符号超时进 errs、其余符号照常输出、整体不超时、facts>0 不返回 err。
func TestSwapFacts_PerCallTimeout(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "pxbt", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &slowFakeMT5Client{
		params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
			"AUDJPY": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 5, SwapShort: -5, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100000}}},
		},
		slowSym:  "AUDJPY",
		delay:    4 * time.Second, // > 3s per-call timeout
		gotCalls: map[string]int{},
	}
	a := &MT5Adapter{venue: "pxbt", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "AUDJPY": "AUDJPY"})

	start := time.Now()
	var fs []fact.Fact
	var err error
	logOut := captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	elapsed := time.Since(start)

	// 整体应在 per-call 超时（3s）+ 少量开销内完成，远小于 4s（慢符号不应拖垮整轮）。
	if elapsed > 3500*time.Millisecond {
		t.Errorf("整体耗时 %v 过长，慢符号应被 per-call 3s 超时隔离", elapsed)
	}
	// facts>0（EURUSD 正常输出）→ 不返回 err（swapResult 语义：errs 只打日志）。
	if err != nil {
		t.Errorf("SwapFacts 应返回 nil err（facts>0），got %v", err)
	}
	// EURUSD swap facts 正常输出。
	if n := countFacts(fs, fact.KindSwapLong); n != 1 {
		t.Errorf("swap_long facts = %d, want 1（EURUSD 正常）", n)
	}
	// AUDJPY 超时进 errs（日志含 AUDJPY + context deadline exceeded）。
	if !strings.Contains(logOut, "AUDJPY") || !strings.Contains(logOut, "context deadline exceeded") {
		t.Errorf("应有 AUDJPY 超时日志，got:\n%s", logOut)
	}
	// 两个符号都被调用过（慢符号虽超时但调用已发起）。
	if client.gotCalls["EURUSD"] == 0 || client.gotCalls["AUDJPY"] == 0 {
		t.Errorf("两符号均应被调用，got %v", client.gotCalls)
	}
}

// TestSwapFacts_PerCallTimeout_AllSlowFails 全部符号超时 → facts 空 + errs>0 → 返回 err（Scheduler 退避重试）。
func TestSwapFacts_PerCallTimeout_AllSlowFails(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "pxbt", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &allSlowMT5Client{
		params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
			"AUDJPY": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 5, SwapShort: -5, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100000}}},
		},
		delay: 4 * time.Second,
	}
	a := &MT5Adapter{venue: "pxbt", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "AUDJPY": "AUDJPY"})

	var fs []fact.Fact
	var err error
	_ = captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	// facts 空 + errs>0 → swapResult 返回 err。
	if err == nil {
		t.Fatalf("全慢应返回 err（facts 空），got nil")
	}
	if len(fs) != 0 {
		t.Errorf("全慢 facts 应空，got %d", len(fs))
	}
}

// allSlowMT5Client 所有符号都慢（模拟 PXBT 全符号超时场景）。
type allSlowMT5Client struct {
	mt5pb.MT5Client
	params   map[string]*mt5pb.SymbolParamsReply
	delay    time.Duration
	leverage float64 // S6 返修：AccountSummary.Leverage
}

func (f *allSlowMT5Client) SymbolParams(ctx context.Context, req *mt5pb.SymbolParamsRequest, _ ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	select {
	case <-time.After(f.delay):
		if r, ok := f.params[req.GetSymbol()]; ok {
			return r, nil
		}
		return nil, errors.New("not found")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// AccountSummary S6 返修：返回预设 leverage（全符号超时场景下 margin fact 仍可发，
// 杠杆缺失不拖垮 swap 采集语义验证）。
func (f *allSlowMT5Client) AccountSummary(_ context.Context, _ *mt5pb.AccountSummaryRequest, _ ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	return &mt5pb.AccountSummaryReply{Result: &mt5pb.AccountSummary{Leverage: f.leverage}}, nil
}

// TestSwapFacts_PerCallTimeout_ParentCtxIntact per-call 超时不影响 parent ctx。
func TestSwapFacts_PerCallTimeout_ParentCtxIntact(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &slowFakeMT5Client{
		params: map[string]*mt5pb.SymbolParamsReply{
			"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
			"SLOW": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
		},
		slowSym:  "SLOW",
		delay:    4 * time.Second,
		gotCalls: map[string]int{},
	}
	a := &MT5Adapter{venue: "v", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "SLOW": "SLOW"})

	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	_, err := a.SwapFacts(parentCtx, q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	// parent ctx 应仍存活（per-call 超时是派生 ctx，不影响 parent）。
	if parentCtx.Err() != nil {
		t.Errorf("parent ctx 不应被 per-call 超时取消，got %v", parentCtx.Err())
	}
}
