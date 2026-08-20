// mt_swap_poll_test.go —— D-060 S1/S3：SwapFacts 跳过可观测（汇总日志+计数）与 trade_mode Fact。
// 用 fake mtapi 客户端构造各 skip 分支（uncalibrated / None+非零矛盾 / result=nil / symbolGroup=nil），
// 断言每轮每 venue 一行汇总日志与 Fact 产出。
package adapter

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
)

// fakeMT5Client 只实现 SwapFacts 用到的 SymbolParams。
type fakeMT5Client struct {
	mt5pb.MT5Client
	params map[string]*mt5pb.SymbolParamsReply
}

func (f *fakeMT5Client) SymbolParams(_ context.Context, req *mt5pb.SymbolParamsRequest, _ ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	if r, ok := f.params[req.GetSymbol()]; ok {
		return r, nil
	}
	return nil, os.ErrNotExist
}

// fakeMT4Client 只实现 SwapFacts 用到的 SymbolParams。
type fakeMT4Client struct {
	mt4pb.MT4Client
	params map[string]*mt4pb.SymbolParamsReply
}

func (f *fakeMT4Client) SymbolParams(_ context.Context, req *mt4pb.SymbolParamsRequest, _ ...grpc.CallOption) (*mt4pb.SymbolParamsReply, error) {
	if r, ok := f.params[req.GetSymbol()]; ok {
		return r, nil
	}
	return nil, os.ErrNotExist
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
	// EURUSD InPoints 归一需报价：预发一条本 venue 报价。
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v5", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		// emit：正常 InPoints
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
		// emit0：SwapNone 真停用（零息 venue 必须可配对）
		"BTCUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SwapNone, TradeMode: mt5pb.TradeMode_TradeMode_Disabled,
		}}},
		// skip：SymInfo_s408 未标定（FxPro GOLD 实证形态）
		"XAUUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SymInfo_s408, SwapLong: -67.9, SwapShort: 27, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}}},
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

	// 汇总行：emit=1 emit0=1 skip=4 + 原因列表（F1）。
	for _, want := range []string{
		"[mt5:v5] swap poll: emit=1 emit0=1 skip=4",
		"XAUUSD: uncalibrated mode 2",
		"ETHUSD: swap none with nonzero rates",
		"NILUSD: result nil",
		"NOGUSD: symbolGroup nil",
	} {
		if !strings.Contains(logOut, want) {
			t.Errorf("summary log missing %q; got:\n%s", want, logOut)
		}
	}

	// swap Fact：emit 2 条 + emit0 2 条（零值对）。
	if n := countFacts(fs, fact.KindSwapLong); n != 2 {
		t.Errorf("swap_long facts = %d, want 2（emit+emit0）", n)
	}
	// trade_mode：emit/emit0/标定 skip 的符号都发；result=nil/symbolGroup=nil 无数据不发（F3）。
	if n := countFacts(fs, fact.KindTradeMode); n != 4 {
		t.Errorf("trade_mode facts = %d, want 4", n)
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

func TestMT4SwapFactsSkipObservability(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v4", VenueType: "mt4", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	ex := func(swapType, enable int32, l, s, point, cs float64) *mt4pb.SymbolInfoEx {
		return &mt4pb.SymbolInfoEx{SwapType: swapType, SwapEnable: enable, SwapLong: l, SwapShort: s, Point: point, ContractSize: cs}
	}
	a := &MT4Adapter{venue: "v4", mt4: &fakeMT4Client{params: map[string]*mt4pb.SymbolParamsReply{
		"EURUSD": {Result: &mt4pb.SymbolParams{Symbol: &mt4pb.SymbolInfo{Ex: ex(0, 1, 1, -1, 0.00001, 100000)}}}, // emit 点数
		"BTCUSD": {Result: &mt4pb.SymbolParams{Symbol: &mt4pb.SymbolInfo{Ex: ex(0, 0, 0, 0, 0, 0)}}},             // emit0 停用+零
		"WTI":    {Result: &mt4pb.SymbolParams{Symbol: &mt4pb.SymbolInfo{Ex: ex(9, 1, 5.5, -51.55, 0, 0)}}},      // skip 超界
		"ETHUSD": {Result: &mt4pb.SymbolParams{Symbol: &mt4pb.SymbolInfo{Ex: ex(2, 0, 0.1, 0, 0, 0)}}},           // skip 停用+非零矛盾
		"NILUSD": {},                                                                                             // skip result=nil
	}}}
	a.SetSymbolMap(map[string]string{
		"EURUSD": "EURUSD", "BTCUSD": "BTCUSDT", "WTI": "WTI", "ETHUSD": "ETHUSDT", "NILUSD": "NILUSD",
	})

	var fs []fact.Fact
	var err error
	logOut := captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	for _, want := range []string{
		"[mt4:v4] swap poll: emit=1 emit0=1 skip=3",
		"WTI: uncalibrated mode 9",
		"ETHUSD: swap disabled with nonzero rates",
		"NILUSD: result nil",
	} {
		if !strings.Contains(logOut, want) {
			t.Errorf("summary log missing %q; got:\n%s", want, logOut)
		}
	}
	// MT4 无 TradeMode 字段 → 不产 trade_mode Fact（未知放行边界）。
	if n := countFacts(fs, fact.KindTradeMode); n != 0 {
		t.Errorf("trade_mode facts = %d, want 0（MT4 proto 无该字段）", n)
	}
	if n := countFacts(fs, fact.KindSwapShort); n != 2 {
		t.Errorf("swap_short facts = %d, want 2（emit+emit0）", n)
	}
}
