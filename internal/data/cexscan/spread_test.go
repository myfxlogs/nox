// 测试：跨所价差计算（mock HTTP server）。
package cexscan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockOKXTickerServer 模拟 OKX market ticker。
func mockOKXTickerServer(t *testing.T, prices map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		instID := r.URL.Query().Get("instId")
		last, ok := prices[instID]
		if !ok {
			json.NewEncoder(w).Encode(map[string]any{"code": "50001", "msg": "not found", "data": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0",
			"msg":  "",
			"data": []map[string]string{{"last": last}},
		})
	}))
}

// mockBinanceTickerServer 模拟 Binance ticker/price。
func mockBinanceTickerServer(t *testing.T, prices map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		symbol := r.URL.Query().Get("symbol")
		price, ok := prices[symbol]
		if !ok {
			http.Error(w, `{"code":-1121,"msg":"Invalid symbol"}`, 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"symbol": symbol, "price": price})
	}))
}

// TestCalcSpreads_OKXCheaper OKX 便宜 → venueBuy=okx, venueSell=binance。
func TestCalcSpreads_OKXCheaper(t *testing.T) {
	okxSrv := mockOKXTickerServer(t, map[string]string{"BTC-USDT": "50000"})
	defer okxSrv.Close()
	bnSrv := mockBinanceTickerServer(t, map[string]string{"BTCUSDT": "50050"})
	defer bnSrv.Close()

	// 用 helper 覆盖 OKX base URL
	ctx := context.Background()
	results, err := calcSpreadsTestable(ctx, okxSrv.Client(), okxSrv.URL, bnSrv.URL, []string{"BTC"})
	if err != nil {
		t.Fatalf("CalcSpreads: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.VenueBuy != "okx" || r.VenueSell != "binance" {
		t.Errorf("venueBuy/Sell = %s/%s, want okx/binance", r.VenueBuy, r.VenueSell)
	}
	if r.PriceBuy != 50000 || r.PriceSell != 50050 {
		t.Errorf("prices = %v/%v, want 50000/50050", r.PriceBuy, r.PriceSell)
	}
	// spread = (50050-50000)/50000 * 10000 = 10 bps
	if r.SpreadBps != 10 {
		t.Errorf("spreadBps = %v, want 10", r.SpreadBps)
	}
}

// TestCalcSpreads_BinanceCheaper Binance 便宜 → venueBuy=binance, venueSell=okx。
func TestCalcSpreads_BinanceCheaper(t *testing.T) {
	okxSrv := mockOKXTickerServer(t, map[string]string{"ETH-USDT": "3003"})
	defer okxSrv.Close()
	bnSrv := mockBinanceTickerServer(t, map[string]string{"ETHUSDT": "3000"})
	defer bnSrv.Close()

	ctx := context.Background()
	results, err := calcSpreadsTestable(ctx, okxSrv.Client(), okxSrv.URL, bnSrv.URL, []string{"ETH"})
	if err != nil {
		t.Fatalf("CalcSpreads: %v", err)
	}
	r := results[0]
	if r.VenueBuy != "binance" || r.VenueSell != "okx" {
		t.Errorf("venueBuy/Sell = %s/%s, want binance/okx", r.VenueBuy, r.VenueSell)
	}
	if r.SpreadBps != 10 {
		t.Errorf("spreadBps = %v, want 10", r.SpreadBps)
	}
}

// TestCalcSpreads_SymbolNormalization 验证 OKX BTC-USDT ↔ Binance BTCUSDT 归一化。
func TestCalcSpreads_SymbolNormalization(t *testing.T) {
	if okxSymbol("BTC") != "BTC-USDT" {
		t.Errorf("okxSymbol(BTC) = %q, want BTC-USDT", okxSymbol("BTC"))
	}
	if binanceSymbol("BTC") != "BTCUSDT" {
		t.Errorf("binanceSymbol(BTC) = %q, want BTCUSDT", binanceSymbol("BTC"))
	}
}

// TestCalcSpreads_SingleError 单币种错误跳过。
func TestCalcSpreads_SingleError(t *testing.T) {
	okxSrv := mockOKXTickerServer(t, map[string]string{"BTC-USDT": "50000"})
	defer okxSrv.Close()
	bnSrv := mockBinanceTickerServer(t, map[string]string{"BTCUSDT": "50050"})
	defer bnSrv.Close()

	ctx := context.Background()
	// BTC 两所都有 → 1 result; UNKNOWN 两所都没有 → 跳过
	results, err := calcSpreadsTestable(ctx, okxSrv.Client(), okxSrv.URL, bnSrv.URL, []string{"BTC", "UNKNOWN"})
	if err != nil {
		t.Fatalf("CalcSpreads: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (UNKNOWN skipped)", len(results))
	}
	if results[0].Symbol != "BTC" {
		t.Errorf("result symbol = %q, want BTC", results[0].Symbol)
	}
}

// calcSpreadsTestable 可注入 OKX base URL 的 CalcSpreads（测试用）。
// 注入基址同时覆写包级直连基址 okxDirectBase——D-011 S2 起 fetchOKXTicker 先打直连域，
// 不覆写会外打生产 www.okx.com（网络可达时拿到真价污染断言，不可达时白耗超时）。
func calcSpreadsTestable(ctx context.Context, client *http.Client, okxBase, bnBase string, symbols []string) ([]SpreadResult, error) {
	old := okxDirectBase
	okxDirectBase = okxBase
	defer func() { okxDirectBase = old }()
	var out []SpreadResult
	for _, ccy := range symbols {
		okxPrice, err := fetchOKXTicker(ctx, client, okxBase, ccy)
		if err != nil {
			continue
		}
		bnPrice, err := fetchBinanceTicker(ctx, client, bnBase, ccy)
		if err != nil {
			continue
		}
		var buy, sell, low, high float64
		var venueBuy, venueSell string
		if okxPrice <= bnPrice {
			buy, sell = okxPrice, bnPrice
			low, high = okxPrice, bnPrice
			venueBuy, venueSell = "okx", "binance"
		} else {
			buy, sell = bnPrice, okxPrice
			low, high = bnPrice, okxPrice
			venueBuy, venueSell = "binance", "okx"
		}
		var bps float64
		if low > 0 {
			bps = (high - low) / low * 10000
		}
		out = append(out, SpreadResult{
			Symbol:    ccy,
			VenueBuy:  venueBuy,
			VenueSell: venueSell,
			PriceBuy:  buy,
			PriceSell: sell,
			SpreadBps: bps,
		})
	}
	return out, nil
}
