// mt_swap_poll_test.go —— D-060 S1/S3：SwapFacts 跳过可观测（汇总日志+计数）与 trade_mode Fact。
// 用 fake mtapi 客户端构造各 skip 分支（uncalibrated / None+非零矛盾 / result=nil / symbolGroup=nil），
// 断言每轮每 venue 一行汇总日志与 Fact 产出。
package adapter

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/fact"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
)

// fakeMT5Client 只实现 SwapFacts 用到的 SymbolParams + AccountSummary（S6 返修）。
// 内嵌 SubscriptionsClient + StreamsClient（nil 接口，仅覆盖需调用的方法），
// 使其可赋值给 a.subs / a.streams；未覆盖方法调用会 panic（测试不触达）。
// SubscribeOrderProfit / OnOrderProfit 覆盖见 mt_margin_source_test.go。
type fakeMT5Client struct {
	mt5pb.MT5Client
	mt5pb.SubscriptionsClient
	mt5pb.StreamsClient
	params     map[string]*mt5pb.SymbolParamsReply
	paramsMany *mt5pb.SymbolParamsManyReply // D-142 ③：MetaFacts 批量拉取 mock
	leverage   float64                      // S6 返修：AccountSummary.Leverage（0=未设/缺省）
	summaryErr error                        // S6 返修：AccountSummary 强制返回 err（nil=正常）
	summaryNil bool                         // S6 返修：AccountSummary 返回 result=nil
	// 事件驱动测试控制（mt_margin_source_test.go 用）：
	// opMu 保护 summaryCalls/subsOPCalls/onOP*（consumeStream goroutine 写 vs waitFor 断言读）。
	opMu         sync.Mutex
	summaryCalls int                                                    // AccountSummary 调用计数（断言防抖/重试）
	summaryFailN int                                                    // 前 N 次 AccountSummary 返回 summaryErr（之后正常返回 leverage）
	subsOPCalls  int                                                    // SubscribeOrderProfit 调用计数
	subsOPErr    error                                                  // SubscribeOrderProfit 强制返回 err（nil=OK）
	onOPCalls    int                                                    // OnOrderProfit 调用计数
	onOPStreams  []grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply] // 流队列（按调用顺序弹出）
	onOPIdx      int
	onOPErr      error // OnOrderProfit 强制返回 err（一次性，优先于队列）
	// OnQuote 流重连测试控制（mt_quote_stream_test.go 用）：
	subMu             sync.Mutex                                       // 保护 subscribedSymbols/subCalls 并发读写
	subscribedSymbols map[string]bool                                  // 被 Subscribe 调用的符号集（resubscribeAll 断言用）
	subCalls          int                                              // Subscribe 调用计数
	subErr            error                                            // Subscribe 强制返回 err（nil=OK）
	onQuoteMu         sync.Mutex                                       // 保护 onQuoteCalls/onQuoteIdx/onQuoteStreams/onQuoteErr
	onQuoteCalls      int                                              // OnQuote 调用计数
	onQuoteStreams    []grpc.ServerStreamingClient[mt5pb.OnQuoteReply] // 流队列（按调用顺序弹出）
	onQuoteIdx        int
	onQuoteErr        error // OnQuote 强制返回 err（一次性，优先于队列）
	// OnOrderUpdate 流测试控制（mt_order_events_test.go 用）：
	subsOUCalls int                                                    // SubscribeOrderUpdate 调用计数
	subsOUErr   error                                                  // SubscribeOrderUpdate 强制返回 err（nil=OK）
	onOUCalls   int                                                    // OnOrderUpdate 调用计数
	onOUStreams []grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply] // 流队列（按调用顺序弹出）
	onOUIdx     int
	onOUErr     error // OnOrderUpdate 强制返回 err（一次性，优先于队列）
}

func (f *fakeMT5Client) SymbolParams(_ context.Context, req *mt5pb.SymbolParamsRequest, _ ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	if r, ok := f.params[req.GetSymbol()]; ok {
		return r, nil
	}
	return nil, os.ErrNotExist
}

// SymbolParamsMany D-142 ③：MetaFacts 批量拉取 mock。paramsMany=nil → 空结果。
func (f *fakeMT5Client) SymbolParamsMany(_ context.Context, _ *mt5pb.SymbolParamsManyRequest, _ ...grpc.CallOption) (*mt5pb.SymbolParamsManyReply, error) {
	if f.paramsMany != nil {
		return f.paramsMany, nil
	}
	return &mt5pb.SymbolParamsManyReply{}, nil
}

// AccountSummary S6 返修：返回预设 leverage / err / result nil 三态。
// summaryCalls 计数 + summaryFailN 前 N 次强制 err（事件驱动首采重试/事件触发测试用）。
func (f *fakeMT5Client) AccountSummary(_ context.Context, _ *mt5pb.AccountSummaryRequest, _ ...grpc.CallOption) (*mt5pb.AccountSummaryReply, error) {
	f.opMu.Lock()
	f.summaryCalls++
	n := f.summaryCalls
	f.opMu.Unlock()
	if f.summaryFailN > 0 && n <= f.summaryFailN {
		return nil, f.summaryErr
	}
	if f.summaryErr != nil && f.summaryFailN == 0 {
		return nil, f.summaryErr
	}
	if f.summaryNil {
		return &mt5pb.AccountSummaryReply{}, nil
	}
	return &mt5pb.AccountSummaryReply{Result: &mt5pb.AccountSummary{Leverage: f.leverage}}, nil
}

// opLoads 原子读 OrderProfit 链三路计数（waitFor 断言用，D-006 S1b race 修复）。
func (f *fakeMT5Client) opLoads() (summary, subs, onOP int) {
	f.opMu.Lock()
	defer f.opMu.Unlock()
	return f.summaryCalls, f.subsOPCalls, f.onOPCalls
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fn()
	return buf.String()
}

func countFacts(fs []fact.Fact, kind string) int {
	n := 0
	for _, f := range fs {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func TestMT5SwapFactsSkipObservability(t *testing.T) {
	q := bus.New()
	// EURUSD InPoints + XAUUSD SymInfo_s408（WP1 补齐=PerLotCurrency）归一均需报价。
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v5", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "v5", VenueType: "mt5", Bid: 2000, Ask: 2000.1, Time: time.Now()})
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		// emit：正常 InPoints
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
		// emit0：SwapNone 真停用（零息 venue 必须可配对）
		"BTCUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SwapNone, TradeMode: mt5pb.TradeMode_TradeMode_Disabled,
		}}},
		// emit：SymInfo_s408 WP1 补齐=PerLotCurrency（标的货币额，FxPro GOLD 实证形态）
		"XAUUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SymInfo_s408, SwapLong: -67.9, SwapShort: 27, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{ContractSize: 100}}},
		// skip：None+非零矛盾（PXBT 实证形态）
		"ETHUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 0.1, TradeMode: mt5pb.TradeMode_TradeMode_LongOnly,
		}}},
		// skip：result=nil
		"NILUSD": {},
		// skip：symbolGroup=nil
		"NOGUSD": {Result: &mt5pb.SymbolParams{}},
	}}}
	a.SetSymbolMap(map[string]string{
		"EURUSD": "EURUSD", "BTCUSD": "BTCUSDT", "XAUUSD": "XAUUSD",
		"ETHUSD": "ETHUSDT", "NILUSD": "NILUSD", "NOGUSD": "NOGUSD",
	})

	var fs []fact.Fact
	var err error
	logOut := captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}

	// 汇总行：emit=2 emit0=1 skip=3 + 原因列表（F1）。
	// WP1：XAUUSD SymInfo_s408 从 skip→emit（mode 2 补齐=PerLotCurrency）。
	for _, want := range []string{
		"[mt5:v5] swap poll: emit=2 emit0=1 skip=3",
		"ETHUSD: swap none with nonzero rates",
		"NILUSD: result nil",
		"NOGUSD: symbolGroup nil",
	} {
		if !strings.Contains(logOut, want) {
			t.Errorf("summary log missing %q; got:\n%s", want, logOut)
		}
	}
	// WP1：未覆盖计数日志——ETHUSD 是 None+非零矛盾（SwapType=0，矛盾数据=Uncalibrated），
	// 计入 uncovered SwapType=0 count=1。
	if !strings.Contains(logOut, "[mt5:v5] [swap] uncovered SwapType=0 count=1") {
		t.Errorf("应有 uncovered SwapType=0 count=1 日志（ETHUSD 矛盾数据）；got:\n%s", logOut)
	}

	// swap Fact：emit 3 条（EURUSD+XAUUSD）+ emit0 1 条（BTCUSD 零值 long）。
	// BTCUSD→BTCUSDT 是 variant 折叠 → native 零值行并列（rawMode=None 归一仍零）→ 4。
	if n := countFacts(fs, fact.KindSwapLong); n != 4 {
		t.Errorf("swap_long facts = %d, want 4（emit×2+emit0 canonical×1+native×1）", n)
	}
	// trade_mode：emit/emit0/标定 skip 的符号都发；result=nil/symbolGroup=nil 无数据不发（F3）。
	// BTCUSD/ETHUSD 双 variant 各加 1 行 native trade_mode → 4+2=6。
	if n := countFacts(fs, fact.KindTradeMode); n != 6 {
		t.Errorf("trade_mode facts = %d, want 6（canonical×4+native variant×2）", n)
	}
	var btcMode *fact.Fact
	for i := range fs {
		if fs[i].Kind == fact.KindTradeMode && fs[i].Symbol == "BTCUSDT" {
			btcMode = &fs[i]
		}
	}
	if btcMode == nil {
		t.Fatal("BTCUSDT trade_mode fact missing")
	}
	if btcMode.Src != "mt5" || btcMode.Unit != fact.UnitMode || btcMode.Venue != "v5" || btcMode.Value != 0 {
		t.Errorf("BTCUSDT trade_mode fact = %+v, want src=mt5 unit=mode venue=v5 value=0（Disabled 透传）", *btcMode)
	}
}

// TestMT5SwapFactsColdStartReturnsFacts R-c1：missingPrice>0 → facts 与 ErrSwapColdStart 同返。
// EURUSD 有报价→正常 emit；GBPUSD InPoints 需报价但无报价→missingPrice=1。
// 期望：返回非 nil facts（EURUSD 的 swap+trade_mode）+ err 含 ErrSwapColdStart + 符号名单。
func TestMT5SwapFactsColdStartReturnsFacts(t *testing.T) {
	q := bus.New()
	// 只发 EURUSD 报价，GBPUSD 缺报价
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v5", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
		"GBPUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD", "GBPUSD": "GBPUSD"})

	var fs []fact.Fact
	var err error
	_ = captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	// R-c1：facts 非 nil（EURUSD 的 facts 仍返回）
	if fs == nil {
		t.Fatal("cold start 应返回非 nil facts（EURUSD 的 facts），got nil")
	}
	// R-c1：err 含 ErrSwapColdStart
	if err == nil || !errors.Is(err, ErrSwapColdStart) {
		t.Fatalf("应返回 ErrSwapColdStart，got %v", err)
	}
	// R-c2：错误消息含符号名单
	if !strings.Contains(err.Error(), "GBPUSD") {
		t.Errorf("错误消息应含缺报价符号 GBPUSD，got %v", err)
	}
	// EURUSD 的 swap facts 应在返回中
	if n := countFacts(fs, fact.KindSwapLong); n < 1 {
		t.Errorf("应含 EURUSD swap_long fact，got %d swap_long facts", n)
	}
}
