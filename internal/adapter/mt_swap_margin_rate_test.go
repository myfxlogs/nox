// mt_swap_margin_rate_test.go —— D-099（2026-08-24）：margin 采集事件驱动化。
// S6 margin 段已从 SwapFacts 删除（回归纯 swap），margin 取值链路抽至 marginPollOnce
// （mt_swap_helpers.go），由 MarginFactSource（mt_margin_source.go）首采与事件触发共用。
// 本文件直接测 marginPollOnce 三用例（值推导 / 无限杠杆 / 失败返回 false）；
// 事件驱动四用例见 mt_margin_source_test.go。
package adapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arb/internal/fact"
)

// TestMarginPollOnce_ValueFromLeverage Leverage=500 → init_margin_rate=0.002，
// Symbol==venue（账户标识非符号），Unit=ratio，src=mt5。
func TestMarginPollOnce_ValueFromLeverage(t *testing.T) {
	client := &fakeMT5Client{leverage: 500}
	a := &MT5Adapter{venue: "v6", mt5: client}

	f, ok := a.marginPollOnce(context.Background())
	if !ok {
		t.Fatal("marginPollOnce ok=false, want true (leverage=500)")
	}
	if f.Kind != fact.KindInitMarginRate {
		t.Errorf("kind = %q, want %q", f.Kind, fact.KindInitMarginRate)
	}
	if f.Value != 0.002 {
		t.Errorf("value = %v, want 0.002 (1/500)", f.Value)
	}
	if f.Symbol != "v6" || f.Venue != "v6" {
		t.Errorf("Symbol=%q Venue=%q, want v6/v6 (账户标识非符号)", f.Symbol, f.Venue)
	}
	if f.Unit != fact.UnitRatio {
		t.Errorf("Unit = %q, want %q", f.Unit, fact.UnitRatio)
	}
	if f.Src != "mt5" {
		t.Errorf("Src = %q, want mt5", f.Src)
	}
}

// TestMarginPollOnce_InfiniteLeverage Leverage=2e9（Exness 无限杠杆哨兵）→ 0 值 fact（发）。
func TestMarginPollOnce_InfiniteLeverage(t *testing.T) {
	client := &fakeMT5Client{leverage: 2e9}
	a := &MT5Adapter{venue: "v6", mt5: client}

	f, ok := a.marginPollOnce(context.Background())
	if !ok {
		t.Fatal("marginPollOnce ok=false, want true (infinite leverage is a fact)")
	}
	if f.Value != 0 {
		t.Errorf("value = %v, want 0 (infinite leverage sentinel → zero margin)", f.Value)
	}
}

// TestMarginPollOnce_LeverageFailReturnsFalse AccountSummary 返回 err → ok=false，
// log 行含 "margin: leverage fetch failed"（WP1 禁静默）。
func TestMarginPollOnce_LeverageFailReturnsFalse(t *testing.T) {
	client := &fakeMT5Client{summaryErr: errors.New("rpc unavailable")}
	a := &MT5Adapter{venue: "v6", mt5: client}

	logOut := captureLog(t, func() {
		if _, ok := a.marginPollOnce(context.Background()); ok {
			t.Error("marginPollOnce ok=true, want false (leverage fetch failed)")
		}
	})
	if !strings.Contains(logOut, "margin: leverage fetch failed") {
		t.Errorf("log should contain 'margin: leverage fetch failed', got: %s", logOut)
	}
}
