// mt_swap_d383_test.go —— D-383 A1/A2/A4/A7：canonical 发射闸（emitBlocked）。
// A1=canonical swap 只来自 primary 行（不再互覆）；A2=primary 值稳定 + 非 primary 行
// native fact 仍在（D-325 语义保留）；A4=双行全 blocked → canonical 停发（MT5 native 照发）；
// A7=MT4 blocked 行整行跳过（canonical 即唯一值）。
package adapter

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// d383SymParams MT5 SymbolParams 构造（PercCurPrice 年化% 模式，价格归一走报价）。
func d383SymParams(long, short float64) *mt5pb.SymbolParamsReply {
	return &mt5pb.SymbolParamsReply{Result: &mt5pb.SymbolParams{
		SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_PercCurPrice, SwapLong: long, SwapShort: short,
			TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		},
		SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100},
	}}
}

// TestD383A1_CanonicalOnlyFromPrimary A1：3001157 XAUUSD 双行（XAUUSDm primary +
// XAUUSD247m variant）——canonical swap 恒=XAUUSDm 值且仅一行（不再互覆）；
// variant native fact 照发（D-325 保留）。
func TestD383A1_CanonicalOnlyFromPrimary(t *testing.T) {
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 4000, Ask: 4000.2, Time: now})
	a := &MT5Adapter{venue: "3001157", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"XAUUSDm":    d383SymParams(-5.349, 1.2),
		"XAUUSD247m": d383SymParams(-9.99, 2.5), // 变体独立值——撞车时 canonical 必被污染（红证可观测）
	}}}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD", "XAUUSD247m": "XAUUSD"})
	a.SetCanonicalEmitBlocked(map[string]bool{"XAUUSD247m": true})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	if n := d325Count(fs, fact.KindSwapLong, "XAUUSD"); n != 1 {
		t.Fatalf("canonical swap_long XAUUSD = %d 行, want 1（红=双行互覆/双写）", n)
	}
	cv, _ := d325Value(fs, fact.KindSwapLong, "XAUUSD")
	wantM := -5.349 / 100 / 365 * 10000
	if math.Abs(cv-wantM) > 1e-9 {
		t.Fatalf("canonical swap_long = %.6f, want XAUUSDm 归一值 %.6f（红=canonical 来自 247m 变体）", cv, wantM)
	}
	nv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD247m")
	want247 := -9.99 / 100 / 365 * 10000
	if !ok || math.Abs(nv-want247) > 1e-9 {
		t.Fatalf("native swap_long XAUUSD247m = %v/%.6f, want 在且为变体 raw 归一值 %.6f（D-325 保留）", ok, nv, want247)
	}
	if n := d325Count(fs, fact.KindTradeMode, "XAUUSD"); n != 1 {
		t.Errorf("canonical trade_mode = %d 行, want 1（blocked 行 canonical trade_mode 停发）", n)
	}
	if n := d325Count(fs, fact.KindTradeMode, "XAUUSD247m"); n != 1 {
		t.Errorf("native trade_mode XAUUSD247m = %d 行, want 1（native 照发）", n)
	}
}

// TestD383A2_GoldGoldM7CanonicalStable A2：GOLD(primary)/GOLDM7(blocked) 双行——
// canonical swap 恒=GOLD 值；GOLDM7 零值 native fact 仍在（swap-free 变体可观测）。
func TestD383A2_GoldGoldM7CanonicalStable(t *testing.T) {
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "39050818", VenueType: "mt5", Bid: 2400, Ask: 2400.1, Time: now})
	a := &MT5Adapter{venue: "39050818", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"GOLD":   d383SymParams(-1.413, 0.818),
		"GOLDM7": d383SymParams(0, 0), // swap-free 变体零值
	}}}
	a.SetSymbolMap(map[string]string{"GOLD": "XAUUSD", "GOLDM7": "XAUUSD"})
	a.SetCanonicalEmitBlocked(map[string]bool{"GOLDM7": true})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	cv, ok := d325Value(fs, fact.KindSwapLong, "XAUUSD")
	want := -1.413 / 100 / 365 * 10000
	if !ok || math.Abs(cv-want) > 1e-9 {
		t.Fatalf("canonical swap_long = %v/%.6f, want GOLD 值 %.6f 稳定（红=被 GOLDM7 零值覆盖）", ok, cv, want)
	}
	if n := d325Count(fs, fact.KindSwapLong, "XAUUSD"); n != 1 {
		t.Errorf("canonical swap_long = %d 行, want 1", n)
	}
	nv, ok := d325Value(fs, fact.KindSwapLong, "GOLDM7")
	if !ok || nv != 0 {
		t.Errorf("native swap_long GOLDM7 = %v/%v, want 在且为 0（swap-free 零值 native 可观测）", ok, nv)
	}
}

// TestD383A4_ZeroPrimaryCanonicalDead A4 发射面：多行组零 primary（双行全 blocked）→
// canonical 全停（MT5 native 照发）；skips 留抑制痕（P4 可观测）。
func TestD383A4_ZeroPrimaryCanonicalDead(t *testing.T) {
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "v", VenueType: "mt5", Bid: 4000, Ask: 4000.2, Time: now})
	a := &MT5Adapter{venue: "v", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"SYM1": d383SymParams(-1, 1),
		"SYM2": d383SymParams(-2, 2),
	}}}
	a.SetSymbolMap(map[string]string{"SYM1": "XAUUSD", "SYM2": "XAUUSD"})
	a.SetCanonicalEmitBlocked(map[string]bool{"SYM1": true, "SYM2": true})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	for _, kind := range []string{fact.KindSwapLong, fact.KindSwapShort, fact.KindTradeMode} {
		if n := d325Count(fs, kind, "XAUUSD"); n != 0 {
			t.Errorf("零 primary 组 canonical %s = %d 行, want 0（fail-dead 宁死勿错配）", kind, n)
		}
	}
	if n := d325Count(fs, fact.KindSwapLong, "SYM1"); n != 1 {
		t.Errorf("native swap_long SYM1 = %d 行, want 1（native 照发）", n)
	}
}
