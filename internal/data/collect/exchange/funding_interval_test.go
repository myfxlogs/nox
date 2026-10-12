package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFundingIntervalCache_PopulateAndLookup fundingInfo 解析+缓存命中/回退。
func TestFundingIntervalCache_PopulateAndLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "PAXGUSDT", "fundingIntervalHours": 4},
			{"symbol": "XAUTUSDT", "fundingIntervalHours": 4},
			{"symbol": "BTCUSDT", "fundingIntervalHours": 8},
		})
	}))
	defer srv.Close()

	cfg := Config{BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewFundingIntervalCache()
	if err := c.Populate(context.Background(), cfg); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	// PAXG 4h → 6 次/日
	if got := c.PeriodsPerDay("PAXGUSDT"); got != 6 {
		t.Errorf("PAXGUSDT 4h want 6/day, got %d", got)
	}
	// XAUT 4h → 6 次/日
	if got := c.PeriodsPerDay("XAUTUSDT"); got != 6 {
		t.Errorf("XAUTUSDT 4h want 6/day, got %d", got)
	}
	// BTC 8h → 3 次/日
	if got := c.PeriodsPerDay("BTCUSDT"); got != 3 {
		t.Errorf("BTCUSDT 8h want 3/day, got %d", got)
	}
	// 未命中 → 回退 3
	if got := c.PeriodsPerDay("ETHUSDT"); got != 3 {
		t.Errorf("ETHUSDT miss want fallback 3, got %d", got)
	}
}

// TestFundingIntervalCache_PAXG4hAnnualized PAXG 4h→periods=6 年化=rate×6×365×100。
func TestFundingIntervalCache_PAXG4hAnnualized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "PAXGUSDT", "fundingIntervalHours": 4},
		})
	}))
	defer srv.Close()

	cfg := Config{BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewFundingIntervalCache()
	if err := c.Populate(context.Background(), cfg); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	periods := c.PeriodsPerDay("PAXGUSDT")
	if periods != 6 {
		t.Fatalf("PAXGUSDT want 6/day, got %d", periods)
	}
	// 年化验证：0.0001 × 6 × 365 × 100 = 21.9%
	rate := 0.0001
	annualized := annualizeFunding(rate, periods)
	want := rate * 6 * 365 * 100
	if annualized != want {
		t.Errorf("PAXG 4h annualized want %.4f, got %.4f", want, annualized)
	}
	// 旧 8h×3 年化 = 10.95%，新 4h×6 = 21.9%，恰 2×
	oldVal := annualizeFunding(rate, 3)
	if annualized != 2*oldVal {
		t.Errorf("PAXG 4h annualized want 2× old (%.4f), got %.4f", 2*oldVal, annualized)
	}
}

// TestFundingIntervalCache_StringEncoding fundingIntervalHours 字符串编码兼容。
func TestFundingIntervalCache_StringEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 某些 API 网关返回字符串编码的数字
		json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "PAXGUSDT", "fundingIntervalHours": "4"},
		})
	}))
	defer srv.Close()

	cfg := Config{BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewFundingIntervalCache()
	if err := c.Populate(context.Background(), cfg); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if got := c.PeriodsPerDay("PAXGUSDT"); got != 6 {
		t.Errorf("string-encoded 4h want 6/day, got %d", got)
	}
}

// TestFundingIntervalCache_FetchError 拉取失败 → 不阻断，回退 3。
func TestFundingIntervalCache_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := Config{BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewFundingIntervalCache()
	if err := c.Populate(context.Background(), cfg); err == nil {
		t.Fatal("Populate should error on 500")
	}
	// 失败后回退 3
	if got := c.PeriodsPerDay("PAXGUSDT"); got != 3 {
		t.Errorf("fetch error want fallback 3, got %d", got)
	}
}

// TestFundingIntervalCache_ZeroHoursSkip fundingIntervalHours=0 → 跳过（不缓存）。
func TestFundingIntervalCache_ZeroHoursSkip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "BADUSDT", "fundingIntervalHours": 0},
			{"symbol": "PAXGUSDT", "fundingIntervalHours": 4},
		})
	}))
	defer srv.Close()

	cfg := Config{BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewFundingIntervalCache()
	if err := c.Populate(context.Background(), cfg); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	// BADUSDT 被跳过 → 回退 3
	if got := c.PeriodsPerDay("BADUSDT"); got != 3 {
		t.Errorf("zero hours skip want fallback 3, got %d", got)
	}
	// PAXGUSDT 正常缓存
	if got := c.PeriodsPerDay("PAXGUSDT"); got != 6 {
		t.Errorf("PAXGUSDT want 6/day, got %d", got)
	}
}
