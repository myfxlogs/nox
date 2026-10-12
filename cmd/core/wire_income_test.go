package main

// D-334c S0/T0：income 边界键空间补试——白名单/OpenLegSymbols 供 canonical（XAU），
// FundingIntervalCache 键=venue pair（XAUUSDT）：miss 时补试 +USDT，两 miss 才 8h 回退。
// 红=现码无补试（生产实证 30 symbols 全量回退）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nox/internal/data/collect/exchange"
)

func TestD334c_IncomeBoundaryUSDTPairRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "XAUUSDT", "fundingIntervalHours": 4}, // XAU→XAUUSDT 补试命中（4h→6/天）
		})
	}))
	defer srv.Close()

	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(context.Background(), exchange.Config{BinanceBaseURL: srv.URL, Client: srv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC) // 10:00 UTC
	openSymbols := func() ([]string, error) { return nil, nil }

	// ① canonical XAU → 补试 XAUUSDT 命中 4h → 下一边界 12:00 UTC +1min
	wait, missing := nextIncomeBoundary(now, intervals, nil, []string{"XAU"}, openSymbols)
	want := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).Add(time.Minute).Sub(now)
	if missing != 0 || wait != want {
		t.Errorf("XAU retry: missing=%d wait=%v, want 0/%v（红=现码无补试落 8h 回退）", missing, wait, want)
	}

	// ② 全 miss（BTCUSDT 不在 fundingInfo）→ 8h 回退 + missing 计数（D-079 禁静默）
	wait2, missing2 := nextIncomeBoundary(now, intervals, nil, []string{"BTC"}, openSymbols)
	want2 := time.Date(2026, 9, 18, 16, 0, 0, 0, time.UTC).Add(time.Minute).Sub(now)
	if missing2 != 1 || wait2 != want2 {
		t.Errorf("all miss: missing=%d wait=%v, want 1/%v（8h 回退回归锚）", missing2, wait2, want2)
	}
}

// TestD334c_PulsedAwareFor T3：demo 域脉动语义开关单源派生（Environment==demo），
// real/未知 env 恒 false=worst 严判一字不动（防未来第三处装配漏注/错注）。
func TestD334c_PulsedAwareFor(t *testing.T) {
	if !pulsedAwareFor("demo") {
		t.Error("demo env: pulsedAware want true")
	}
	if pulsedAwareFor("live") {
		t.Error("live env: pulsedAware want false（real 域铁边界）")
	}
	if pulsedAwareFor("") {
		t.Error("empty env: pulsedAware want false")
	}
}

// TestD466_IncomeBoundaryVenueNativeSettle venue 原生 next_settle fact 优先于
// interval 边界（XAUUSDT settle=11:30 实测漂移 → 边界=11:31 非 12:01）；
// fact 缺席品种仍走 interval 路径（混合共存）。
func TestD466_IncomeBoundaryVenueNativeSettle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "XAUUSDT", "fundingIntervalHours": 4},
		})
	}))
	defer srv.Close()
	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(context.Background(), exchange.Config{BinanceBaseURL: srv.URL, Client: srv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	openSymbols := func() ([]string, error) { return nil, nil }
	settles := map[string]time.Time{
		"XAUUSDT": time.Date(2026, 9, 18, 11, 30, 0, 0, time.UTC), // venue 原生漂移时刻
	}
	wait, missing := nextIncomeBoundary(now, intervals, settles, []string{"XAU", "BTC"}, openSymbols)
	want := time.Date(2026, 9, 18, 11, 30, 0, 0, time.UTC).Add(time.Minute).Sub(now)
	if wait != want {
		t.Errorf("venue settle boundary: wait=%v want %v（红=仍取 interval 12:00 边界）", wait, want)
	}
	if missing != 1 { // BTC 双缺
		t.Errorf("missing=%d want 1", missing)
	}
	// 过期 fact（next≤now）不取——回落 interval 边界
	settles["XAUUSDT"] = now.Add(-time.Hour)
	wait2, _ := nextIncomeBoundary(now, intervals, settles, []string{"XAU"}, openSymbols)
	want2 := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).Add(time.Minute).Sub(now)
	if wait2 != want2 {
		t.Errorf("expired fact: wait=%v want %v（interval 边界回落）", wait2, want2)
	}
}
