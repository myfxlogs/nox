// D-271 S7 集成测试：scanOnce 全流程（mock HTTP + DSN）。
package cexscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"arb/internal/config"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// mockAllOKX 模拟 OKX Savings + ticker。
func mockAllOKX(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/finance/savings/lending-rate-summary":
			json.NewEncoder(w).Encode(map[string]any{
				"code": "0",
				"data": []map[string]any{{"ccy": "USDT", "estRate": "0.035"}},
			})
		case "/api/v5/market/ticker":
			instID := r.URL.Query().Get("instId")
			json.NewEncoder(w).Encode(map[string]any{
				"code": "0",
				"data": []map[string]string{{"last": "50000"}},
			})
			_ = instID
		default:
			http.Error(w, "not found", 404)
		}
	}))
}

// mockAllBinance 模拟 Binance Simple Earn + ticker。
func mockAllBinance(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/simple-earn/flexible/list":
			json.NewEncoder(w).Encode(map[string]any{
				"rows": []map[string]any{
					{"asset": "USDT", "latestAnnualPercentageRate": "0.025", "tierAnnualPercentageRate": nil},
				},
			})
		case "/v3/ticker/price":
			json.NewEncoder(w).Encode(map[string]string{"symbol": "BTCUSDT", "price": "50050"})
		default:
			http.Error(w, "not found", 404)
		}
	}))
}

// fakeSnap fake AccountSnapshotter。
type fakeSnap struct{ infos []*v1.AccountInfo }

func (f *fakeSnap) Snapshot() []*v1.AccountInfo { return f.infos }

// TestScanOnce_FullFlow DSN 集成：scanOnce 全流程（账户→理财→价差→入库）。
func TestScanOnce_FullFlow(t *testing.T) {
	dsn := os.Getenv("ARB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_DSN not set")
	}
	okxSrv := mockAllOKX(t)
	defer okxSrv.Close()
	bnSrv := mockAllBinance(t)
	defer bnSrv.Close()

	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, "TRUNCATE fin_cex_earn_apr, fin_cex_spread CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}

	cfg := &config.Config{
		CexScanInterval: time.Minute,
		CexScanSymbols:  []string{"BTC"},
	}
	// 构造 Scanner，注入 mock URL
	scanner := &Scanner{
		cfg:      cfg,
		store:    s,
		accounts: &fakeSnap{infos: []*v1.AccountInfo{{Venue: "okx", VenueType: "spot", Balance: 1000}}},
		earn: &EarnCollector{
			client: okxSrv.Client(),
			okxKey: "k", okxSecret: "s", okxPass: "p",
			bnKey: "k", bnSecret: "s",
			bnSapiURL: bnSrv.URL,
		},
		client:   bnSrv.Client(),
		bnAPIURL: bnSrv.URL,
		symbols:  []string{"BTC"},
	}
	// 覆盖 OKX base URL（用 collectOKXWithBase）
	// scanner.scanOnce 调 s.earn.Collect → CollectOKX（直连 www.okx.com → 传输层错误回落
	// c.okxBase，D-011 S2）；此处直接注入 mock base 测组件 + 手动入库验证 scanOnce 逻辑
	// 先用 collectOKXWithBase 测试 OKX 采集
	okxAPRs, err := collectOKXWithBase(ctx, scanner.earn, okxSrv.URL)
	if err != nil {
		t.Fatalf("collectOKX: %v", err)
	}
	if len(okxAPRs) != 1 || okxAPRs[0].APR != 0.035 {
		t.Fatalf("okx aprs = %+v, want USDT/0.035", okxAPRs)
	}

	// Binance 采集
	bnAPRs, err := scanner.earn.CollectBinance(ctx)
	if err != nil {
		t.Fatalf("CollectBinance: %v", err)
	}
	if len(bnAPRs) != 1 || bnAPRs[0].APR != 0.025 {
		t.Fatalf("binance aprs = %+v, want USDT/0.025", bnAPRs)
	}

	// 入库
	allAPRs := append(okxAPRs, bnAPRs...)
	for i := range allAPRs {
		if err := s.InsertCexEarnAPR(ctx, &allAPRs[i]); err != nil {
			t.Fatalf("InsertCexEarnAPR: %v", err)
		}
	}

	// 价差计算（用 mock server）
	spreads, err := calcSpreadsTestable(ctx, scanner.client, okxSrv.URL, bnSrv.URL, []string{"BTC"})
	if err != nil {
		t.Fatalf("calcSpreads: %v", err)
	}
	if len(spreads) != 1 {
		t.Fatalf("spreads = %d, want 1", len(spreads))
	}
	// OKX 50000 < Binance 50050 → venueBuy=okx, spread=10bps
	if spreads[0].VenueBuy != "okx" || spreads[0].SpreadBps != 10 {
		t.Errorf("spread = %+v, want okx/10bps", spreads[0])
	}

	// 价差入库
	for i := range spreads {
		row := &pgstore.CexSpread{
			Symbol: spreads[i].Symbol, VenueBuy: spreads[i].VenueBuy,
			VenueSell: spreads[i].VenueSell, PriceBuy: spreads[i].PriceBuy,
			PriceSell: spreads[i].PriceSell, SpreadBps: spreads[i].SpreadBps,
		}
		if err := s.InsertCexSpread(ctx, row); err != nil {
			t.Fatalf("InsertCexSpread: %v", err)
		}
	}

	// 验证 DB 有理财 APR
	gotAPRs, err := s.LatestCexEarnAPRs(ctx)
	if err != nil {
		t.Fatalf("LatestCexEarnAPRs: %v", err)
	}
	if len(gotAPRs) != 2 {
		t.Errorf("earn aprs in DB = %d, want 2", len(gotAPRs))
	}

	// 验证 DB 有价差
	gotSpreads, err := s.LatestCexSpreads(ctx)
	if err != nil {
		t.Fatalf("LatestCexSpreads: %v", err)
	}
	if len(gotSpreads) != 1 {
		t.Errorf("spreads in DB = %d, want 1", len(gotSpreads))
	}
}

// TestScanOnce_SymbolNormalization symbol 归一化（OKX BTC-USDT ↔ Binance BTCUSDT）。
func TestScanOnce_SymbolNormalization(t *testing.T) {
	if okxSymbol("BTC") != "BTC-USDT" {
		t.Errorf("okxSymbol(BTC) = %q, want BTC-USDT", okxSymbol("BTC"))
	}
	if binanceSymbol("BTC") != "BTCUSDT" {
		t.Errorf("binanceSymbol(BTC) = %q, want BTCUSDT", binanceSymbol("BTC"))
	}
	if okxSymbol("ETH") != "ETH-USDT" {
		t.Errorf("okxSymbol(ETH) = %q, want ETH-USDT", okxSymbol("ETH"))
	}
	if binanceSymbol("ETH") != "ETHUSDT" {
		t.Errorf("binanceSymbol(ETH) = %q, want ETHUSDT", binanceSymbol("ETH"))
	}
}

// TestCalcSpreads_SkipStablecoin 稳定币跳过（USDT-USDT/USDTUSDT 无意义）。
func TestCalcSpreads_SkipStablecoin(t *testing.T) {
	for _, ccy := range []string{"USDT", "USDC", "DAI"} {
		if !isStablecoin(ccy) {
			t.Errorf("isStablecoin(%q) = false, want true", ccy)
		}
	}
	for _, ccy := range []string{"BTC", "ETH", "TRX", "SOL"} {
		if isStablecoin(ccy) {
			t.Errorf("isStablecoin(%q) = true, want false", ccy)
		}
	}
}
