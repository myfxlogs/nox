// mt_swap_margin_rate_test.go —— S6 返修（2026-08-24）：账户级杠杆 → init_margin_rate。
// 数据源变更：SymGroup.InitMarginRate/MntnMarginRate 网关不返回（proto 定义≠实装），
// 改取 AccountSummary.Leverage → init_margin_rate = 1/杠杆（无限杠杆哨兵=0）。
// 维持保证金率（mntn）无数据源，Kind 已删，本文件不含 mntn 用例。
package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// marginParamsFixture 构造 EURUSD SymbolParams（InPoints 标定可发 swap fact，
// 确保 SwapFacts 不因 swap 全失败而 short-circuit margin 断言）。
func marginParamsFixture() map[string]*mt5pb.SymbolParamsReply {
	return map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}
}

// TestSwapFacts_MarginRateFromLeverage Leverage=500 → init_margin_rate=0.002，
// Symbol==venue（账户标识非符号），Unit=ratio，src=mt5。
func TestSwapFacts_MarginRateFromLeverage(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: marginParamsFixture(), leverage: 500}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	var mf *fact.Fact
	for i := range fs {
		if fs[i].Kind == fact.KindInitMarginRate {
			mf = &fs[i]
			break
		}
	}
	if mf == nil {
		t.Fatal("init_margin_rate fact missing")
	}
	if mf.Value != 0.002 {
		t.Errorf("init_margin_rate value = %v, want 0.002 (1/500)", mf.Value)
	}
	if mf.Symbol != "v6" {
		t.Errorf("init_margin_rate Symbol = %q, want %q (venue=账户标识)", mf.Symbol, "v6")
	}
	if mf.Venue != "v6" {
		t.Errorf("init_margin_rate Venue = %q, want %q", mf.Venue, "v6")
	}
	if mf.Unit != fact.UnitRatio {
		t.Errorf("init_margin_rate Unit = %q, want %q", mf.Unit, fact.UnitRatio)
	}
	if mf.Src != "mt5" {
		t.Errorf("init_margin_rate Src = %q, want mt5", mf.Src)
	}
}

// TestSwapFacts_MarginRateInfiniteLeverage Leverage=2e9（Exness 无限杠杆哨兵）→ 0 值 fact（发）。
func TestSwapFacts_MarginRateInfiniteLeverage(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: marginParamsFixture(), leverage: 2e9}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	var mf *fact.Fact
	for i := range fs {
		if fs[i].Kind == fact.KindInitMarginRate {
			mf = &fs[i]
			break
		}
	}
	if mf == nil {
		t.Fatal("init_margin_rate fact should be emitted for infinite leverage (zero-margin account is a fact)")
	}
	if mf.Value != 0 {
		t.Errorf("init_margin_rate value = %v, want 0 (infinite leverage sentinel → zero margin)", mf.Value)
	}
}

// TestSwapFacts_MarginRateLeverageFailNoErr AccountSummary 返回 err → 无 margin fact、
// SwapFacts 不返回 err（swap facts 照常）、log 行含 "margin: leverage fetch failed"。
func TestSwapFacts_MarginRateLeverageFailNoErr(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: marginParamsFixture(), summaryErr: errors.New("rpc unavailable")}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	logOut := captureLog(t, func() {
		fs, err := a.SwapFacts(context.Background(), q)
		if err != nil {
			t.Fatalf("SwapFacts should not return err when leverage fetch fails (must not hurt swapResult): %v", err)
		}
		for _, f := range fs {
			if f.Kind == fact.KindInitMarginRate {
				t.Errorf("init_margin_rate fact should NOT be emitted when leverage fetch fails, got %+v", f)
			}
		}
		// swap facts 照常（确保杠杆缺失不拖垮 swap 采集）
		if countFacts(fs, fact.KindSwapLong) == 0 {
			t.Error("swap_long fact should still be emitted when leverage fetch fails")
		}
	})
	if !strings.Contains(logOut, "margin: leverage fetch failed") {
		t.Errorf("log should contain 'margin: leverage fetch failed', got: %s", logOut)
	}
}
