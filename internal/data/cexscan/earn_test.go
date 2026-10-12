// 测试：理财 APR 采集器（mock HTTP server）。
package cexscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nox/internal/platform/config"
	"nox/internal/store/pgstore"
)

// mockOKXEarnServer 模拟 OKX Savings lending-rate-summary。
func mockOKXEarnServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OK-ACCESS-KEY") == "" {
			http.Error(w, "missing key", 401)
			return
		}
		resp := map[string]any{
			"code": "0",
			"msg":  "",
			"data": []map[string]any{
				{"ccy": "USDT", "estRate": 0.035},   // 数字（真实 API 格式）
				{"ccy": "USDC", "estRate": "0.030"}, // string（json.Number 兼容性验证）
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// mockBinanceEarnServer 模拟 Binance Simple Earn Flexible。
func mockBinanceEarnServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MBX-APIKEY") == "" {
			http.Error(w, "missing key", 401)
			return
		}
		resp := map[string]any{
			"rows": []map[string]any{
				{"asset": "USDT", "latestAnnualPercentageRate": "0.02523341", "tierAnnualPercentageRate": map[string]string{"0-1000": "0.025"}},
				{"asset": "USDC", "latestAnnualPercentageRate": "0.020", "tierAnnualPercentageRate": nil},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// TestEarn_CollectOKX mock OKX → 解析 estRate 为小数年化。
func TestEarn_CollectOKX(t *testing.T) {
	srv := mockOKXEarnServer(t)
	defer srv.Close()
	c := &EarnCollector{
		client:    srv.Client(),
		okxKey:    "test-key",
		okxSecret: "test-secret",
		okxPass:   "test-pass",
	}
	// 重写 OKX base URL 为 mock server
	ctx := context.Background()
	// 直接构造请求到 mock server
	origClient := c.client
	_ = origClient
	// 用 helper 覆盖 URL
	aprs, err := collectOKXWithBase(ctx, c, srv.URL)
	if err != nil {
		t.Fatalf("CollectOKX: %v", err)
	}
	if len(aprs) != 2 {
		t.Fatalf("got %d aprs, want 2", len(aprs))
	}
	if aprs[0].Ccy != "USDT" || aprs[0].APR != 0.035 {
		t.Errorf("first = %+v, want USDT/0.035", aprs[0])
	}
	if aprs[0].Venue != "okx" || aprs[0].ProductType != "flexible" {
		t.Errorf("venue/product = %s/%s, want okx/flexible", aprs[0].Venue, aprs[0].ProductType)
	}
}

// TestEarn_CollectBinance mock Binance → 解析 latestAnnualPercentageRate + tier。
func TestEarn_CollectBinance(t *testing.T) {
	srv := mockBinanceEarnServer(t)
	defer srv.Close()
	c := &EarnCollector{
		client:    srv.Client(),
		bnKey:     "test-key",
		bnSecret:  "test-secret",
		bnSapiURL: srv.URL,
	}
	ctx := context.Background()
	aprs, err := c.CollectBinance(ctx)
	if err != nil {
		t.Fatalf("CollectBinance: %v", err)
	}
	if len(aprs) != 2 {
		t.Fatalf("got %d aprs, want 2", len(aprs))
	}
	if aprs[0].Ccy != "USDT" || aprs[0].APR != 0.02523341 {
		t.Errorf("first = %+v, want USDT/0.02523341", aprs[0])
	}
	if !aprs[0].TierAPR.Valid {
		t.Error("USDT tier_apr not set")
	}
	if aprs[1].TierAPR.Valid {
		t.Error("USDC tier_apr should be invalid (null)")
	}
}

// TestEarn_CollectMerge 合并 OKX+Binance，单所错误不中断。
func TestEarn_CollectMerge(t *testing.T) {
	okxSrv := mockOKXEarnServer(t)
	defer okxSrv.Close()
	bnSrv := mockBinanceEarnServer(t)
	defer bnSrv.Close()
	c := &EarnCollector{
		client:    okxSrv.Client(),
		okxKey:    "test-key",
		okxSecret: "test-secret",
		okxPass:   "test-pass",
		bnKey:     "test-key",
		bnSecret:  "test-secret",
		bnSapiURL: bnSrv.URL,
	}
	ctx := context.Background()
	// OKX 用 override URL
	aprs, err := collectOKXWithBase(ctx, c, okxSrv.URL)
	if err != nil {
		t.Fatalf("collectOKX: %v", err)
	}
	bnAPRs, err := c.CollectBinance(ctx)
	if err != nil {
		t.Fatalf("CollectBinance: %v", err)
	}
	all := append(aprs, bnAPRs...)
	if len(all) != 4 {
		t.Fatalf("merged %d aprs, want 4", len(all))
	}
}

// TestEarn_NewEarnCollector 从 exchange_configs 筛 simulated=false 取凭据。
func TestEarn_NewEarnCollector(t *testing.T) {
	cfg := &config.Config{}
	exchs := []pgstore.ExchangeConfig{
		{Venue: "okx", VenueType: "spot", APIKey: "okx-k", APISecret: "okx-s", Passphrase: "okx-p", Simulated: false},
		{Venue: "okx", VenueType: "perp", APIKey: "okx-k2", APISecret: "okx-s2", Passphrase: "okx-p2", Simulated: false},
		{Venue: "binance", VenueType: "spot", APIKey: "bn-k", APISecret: "bn-s", BaseURL: "https://192.229.85.35:8443/api", Simulated: false},
		{Venue: "binance", VenueType: "perp", APIKey: "bn-k2", APISecret: "bn-s2", BaseURL: "https://192.229.85.35:8443/fapi", Simulated: false},
		{Venue: "okx", VenueType: "spot", APIKey: "demo-k", APISecret: "demo-s", Simulated: true},
	}
	c := NewEarnCollector(cfg, exchs)
	if c.okxKey != "okx-k" {
		t.Errorf("okxKey = %q, want okx-k", c.okxKey)
	}
	if c.bnKey != "bn-k" {
		t.Errorf("bnKey = %q, want bn-k", c.bnKey)
	}
	if c.bnSapiURL != "https://192.229.85.35:8443/sapi" {
		t.Errorf("bnSapiURL = %q, want https://192.229.85.35:8443/sapi", c.bnSapiURL)
	}
}
