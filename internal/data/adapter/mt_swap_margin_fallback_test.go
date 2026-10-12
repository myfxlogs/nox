package adapter

// 锚：adapter native variant 双发。
// T2=brokerSym!=canon 时 facts 含 canonical+native 两行且值一致（非 Exness 无 override）；
// T3=Exness venue 的 canonical 走 D-300 S0 override 归零，native 保持 raw 值（判据生命线）。

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// d325Count 按 kind+symbol 计数（countFacts 只按 kind，双发断言需细分 symbol）。
func d325Count(fs []fact.Fact, kind, symbol string) int {
	n := 0
	for _, f := range fs {
		if f.Kind == kind && f.Symbol == symbol {
			n++
		}
	}
	return n
}

// d325Value 取 kind+symbol 首个 fact 的 value。
func d325Value(fs []fact.Fact, kind, symbol string) (float64, bool) {
	for _, f := range fs {
		if f.Kind == kind && f.Symbol == symbol {
			return f.Value, true
		}
	}
	return 0, false
}

// TestD325_SwapFacts_DualEmission T2：XAUUSDm→XAUUSD variant 折叠 + EURUSD 无折叠——
// variant 双发（canonical+native 值一致），无折叠符号零重复行。
func TestD325_SwapFacts_DualEmission(t *testing.T) {
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "v5", VenueType: "mt5", Bid: 4000, Ask: 4000.2, Time: now})
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v5", VenueType: "mt5", Bid: 1.08, Ask: 1.0802, Time: now})
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSDm": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -5.349, SwapShort: 1.2, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100}}},
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -2.0, SwapShort: -1.0, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD", "EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	// 双发：canonical + native 各一行（swap_long/swap_short/trade_mode 三 kind 同据）
	for _, tc := range []struct{ kind string }{
		{fact.KindSwapLong}, {fact.KindSwapShort}, {fact.KindTradeMode},
	} {
		if n := d325Count(fs, tc.kind, "XAUUSD"); n != 1 {
			t.Errorf("%s XAUUSD = %d, want 1 (canonical)", tc.kind, n)
		}
		if n := d325Count(fs, tc.kind, "XAUUSDm"); n != 1 {
			t.Errorf("%s XAUUSDm = %d, want 1 (native variant)", tc.kind, n)
		}
	}
	// 值一致（非 Exness 无 override：canonical==native==raw 归一值）
	cv, _ := d325Value(fs, fact.KindSwapLong, "XAUUSD")
	nv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSDm")
	if !ok {
		t.Fatal("native swap_long 缺失")
	}
	if want := -5.349 / 100 / 365 * 10000; math.Abs(nv-want) > 1e-9 || math.Abs(cv-want) > 1e-9 {
		t.Errorf("swap_long value canonical=%.6f native=%.6f, want %.6f（AnnualPct 归一）", cv, nv, want)
	}
	// 无折叠符号（canon==brokerSym）不双发：EURUSD 各 kind 恰 1 行
	for _, tc := range []struct{ kind string }{
		{fact.KindSwapLong}, {fact.KindSwapShort}, {fact.KindTradeMode},
	} {
		if n := d325Count(fs, tc.kind, "EURUSD"); n != 1 {
			t.Errorf("%s EURUSD = %d, want 1（canon==brokerSym 跳过 native 防重复）", tc.kind, n)
		}
	}
}

// TestD325_SwapFacts_ExnessNativeRaw T3：Exness venue（D-300 S0 override）——
// canonical swap 归零，native 保持 raw 值（XAUUSDm 周末腿判据生命线：raw 才能分辨 variant 差异）。
func TestD325_SwapFacts_ExnessNativeRaw(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 4000, Ask: 4000.2, Time: time.Now()})
	a := &MT5Adapter{venue: "3001157", server: "Exness-MT5Real", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSDm": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: -534.9, SwapShort: 13.0, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100}}},
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	// canonical：override 后零值路径
	if cv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD"); !ok || cv != 0 {
		t.Errorf("canonical swap_long = %.4f, want 0（D-300 S0 override）", cv)
	}
	if cv, ok := d325Value(fs, fact.KindSwapShort, "XAUUSD"); !ok || cv != 0 {
		t.Errorf("canonical swap_short = %.4f, want 0（override）", cv)
	}
	// native：raw 值（AnnualPct 归一 = s/100/365×1e4），非零且与 raw 同号
	if nv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSDm"); !ok {
		t.Fatal("native swap_long 缺失")
	} else if want := -534.9 / 100 / 365 * 10000; math.Abs(nv-want) > 1e-6 {
		t.Errorf("native swap_long = %.4f, want %.4f（raw -534.9 归一，非 0）", nv, want)
	}
	if nv, ok := d325Value(fs, fact.KindSwapShort, "XAUUSDm"); !ok {
		t.Fatal("native swap_short 缺失")
	} else if want := 13.0 / 100 / 365 * 10000; math.Abs(nv-want) > 1e-6 {
		t.Errorf("native swap_short = %.4f, want %.4f", nv, want)
	}
	// trade_mode 双发在 override 语义外仍成立
	if n := d325Count(fs, fact.KindTradeMode, "XAUUSDm"); n != 1 {
		t.Errorf("native trade_mode = %d, want 1", n)
	}
}
