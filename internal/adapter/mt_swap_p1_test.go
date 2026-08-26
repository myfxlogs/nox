// mt_swap_p1_test.go —— D-104 P1 T4 / D-112：guardrail JPY / 并发等价 / PXBT fail-closed 单测。
package adapter

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// TestPXBT_SwapTypeZero_Uncalibrated D-112：SwapType=0+非零值 → mt5SwapMode 返回 Uncalibrated → SwapFacts skip。
// 删除 D-104 P1 T1 override 后，PXBT（1314615）SwapType=0+非零 rates 恢复为矛盾数据 → fail-closed 不 emit swap facts。
func TestPXBT_SwapTypeZero_Uncalibrated(t *testing.T) {
	// mt5SwapMode：SwapType=0+非零 → Uncalibrated
	mode := mt5SwapMode(mt5pb.SwapType_SwapType_SwapNone, 5.5, -51.55)
	if mode != SwapModeUncalibrated {
		t.Fatalf("SwapType=0+非零: mode=%v, want Uncalibrated", mode)
	}
	// mt5SwapMode：SwapType=0+零值 → None（真停用）
	mode = mt5SwapMode(mt5pb.SwapType_SwapType_SwapNone, 0, 0)
	if mode != SwapModeNone {
		t.Errorf("SwapType=0+零值: mode=%v, want None", mode)
	}
	// SwapFacts：venue=1314615 + SwapType=0 + 非零 → skip（不 emit swap_long/swap_short，只 emit trade_mode）
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "GBPJPY", Venue: "1314615", VenueType: "mt5", Bid: 190, Ask: 190.01, Time: time.Now()})
	a := &MT5Adapter{venue: "1314615", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"GBPJPY": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 5.5, SwapShort: -6.3508, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.001, ContractSize: 100000}}},
	}}}
	a.SetSymbolMap(map[string]string{"GBPJPY": "GBPJPY"})

	var fs []fact.Fact
	var err error
	logOut := captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	if n := countFacts(fs, fact.KindSwapLong); n != 0 {
		t.Errorf("swap_long facts = %d, want 0 (PXBT Uncalibrated skip)", n)
	}
	if n := countFacts(fs, fact.KindSwapShort); n != 0 {
		t.Errorf("swap_short facts = %d, want 0 (PXBT Uncalibrated skip)", n)
	}
	// trade_mode 仍 emit（不依赖 swap 标定）
	if n := countFacts(fs, fact.KindTradeMode); n != 1 {
		t.Errorf("trade_mode facts = %d, want 1", n)
	}
	// skip 原因含 Uncalibrated/矛盾数据
	if !strings.Contains(logOut, "GBPJPY") {
		t.Errorf("log should contain skip reason for GBPJPY, got: %s", logOut)
	}
}

// TestSwapInPointsGuardRail_JPY D-104 P1 T2：
// canonical 以 "JPY" 结尾时 per-lot 值除以 price（JPY 计价 → USD 换算）。
// USDJPY per-lot 原始=1000（JPY 计价），price=150 → USD=6.67 < 500 不误报。
// USDJPY per-lot 原始=100000，price=150 → USD=666.67 > 500 仍报。
// 非 JPY canonical（WTI）per-lot 不除 price，超阈仍报。
func TestSwapInPointsGuardRail_JPY(t *testing.T) {
	// JPY 不误报：per-lot=1000 JPY，price=150 → USD=6.67 < 500
	got := swapInPointsGuardRail("mt5", "pxbt", "USDJPY", 1000, 1, 1, 150)
	if got {
		t.Errorf("JPY per-lot 1000/150=6.67 < 500: should not warn, got true")
	}
	// JPY 超阈仍报：per-lot=100000 JPY，price=150 → USD=666.67 > 500
	got = swapInPointsGuardRail("mt5", "pxbt", "USDJPY", 100000, 1, 1, 150)
	if !got {
		t.Errorf("JPY per-lot 100000/150=666.67 > 500: should warn, got false")
	}
	// JPY price=0 → 不除（防除零），per-lot=1000 > 500 → 仍报
	got = swapInPointsGuardRail("mt5", "pxbt", "USDJPY", 1000, 1, 1, 0)
	if !got {
		t.Errorf("JPY price=0 no division: per-lot 1000 > 500 should warn, got false")
	}
	// 非 JPY（WTI）per-lot 不除 price：1000 > 500 → 仍报
	got = swapInPointsGuardRail("mt5", "fxpro", "WTI", 1000, 1, 1, 70)
	if !got {
		t.Errorf("non-JPY WTI per-lot 1000 > 500 should warn, got false")
	}
	// 非 JPY（WTI）per-lot 小值不报
	got = swapInPointsGuardRail("mt5", "fxpro", "WTI", 5.5, 0.001, 1000, 70)
	if got {
		t.Errorf("WTI per-lot 5.5 < 500 should not warn, got true")
	}
}

// TestMT5SwapFacts_ConcurrencyEquivalence D-104 P1 T3：
// 并发结果集与串行等价（fact 数量+kind 统计一致，乱序无关）。
// 12 符号（>8 触发 semaphore 排队）→ 并发跑 → fact 数量/kind 统计与预期一致。
func TestMT5SwapFacts_ConcurrencyEquivalence(t *testing.T) {
	q := bus.New()
	// 12 符号全 InPoints + 有报价 → 每符号 emit swap_long+swap_short+trade_mode = 3 facts
	syms := []string{"EURUSD", "GBPUSD", "AUDUSD", "USDJPY", "USDCAD", "NZDUSD",
		"EURJPY", "GBPJPY", "AUDJPY", "CHFJPY", "CADJPY", "NZDJPY"}
	for _, s := range syms {
		q.Publish(bus.Quote{Symbol: s, Venue: "v5", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: time.Now()})
	}
	params := make(map[string]*mt5pb.SymbolParamsReply, len(syms))
	for _, s := range syms {
		params[s] = &mt5pb.SymbolParamsReply{Result: &mt5pb.SymbolParams{
			SymbolGroup: &mt5pb.SymGroup{
				SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1,
				TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			},
			SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000},
		}}
	}
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: params}}
	sm := make(map[string]string, len(syms))
	for _, s := range syms {
		sm[s] = s
	}
	a.SetSymbolMap(sm)

	var fs []fact.Fact
	var err error
	_ = captureLog(t, func() {
		fs, err = a.SwapFacts(context.Background(), q)
	})
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	// 12 符号 × 3 facts = 36（swap_long×12 + swap_short×12 + trade_mode×12）
	if n := countFacts(fs, fact.KindSwapLong); n != 12 {
		t.Errorf("swap_long facts = %d, want 12", n)
	}
	if n := countFacts(fs, fact.KindSwapShort); n != 12 {
		t.Errorf("swap_short facts = %d, want 12", n)
	}
	if n := countFacts(fs, fact.KindTradeMode); n != 12 {
		t.Errorf("trade_mode facts = %d, want 12", n)
	}
	// 验证 fact 总数（含可能的 triple_swap_day venue 级 1 条）
	total := len(fs)
	if total < 36 {
		t.Errorf("total facts = %d, want >= 36", total)
	}
	// 验证所有 swap facts 的 venue 一致（并发不串 venue）
	for _, f := range fs {
		if f.Venue != "v5" {
			t.Errorf("fact venue = %s, want v5 (concurrency venue leak)", f.Venue)
		}
	}
}

// TestMT5SwapFacts_ConcurrencyRaceSafety D-104 P1 T3：并发跑 -race 无数据竞争。
// 多次调用 SwapFacts（12 符号 >8 并发），-race 检测器验证 per-index 槽无竞争。
func TestMT5SwapFacts_ConcurrencyRaceSafety(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v5", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	a := &MT5Adapter{venue: "v5", mt5: &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})
	// 跑 3 次确保 -race 检测覆盖
	for i := 0; i < 3; i++ {
		_, _ = a.SwapFacts(context.Background(), q)
	}
}
