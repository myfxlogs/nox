// D-271 S4 跨所价差计算：OKX vs Binance spot 公开行情。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
)

// SpreadResult 跨所价差结果。
type SpreadResult struct {
	Symbol    string
	VenueBuy  string
	VenueSell string
	PriceBuy  float64
	PriceSell float64
	SpreadBps float64
}

// okxSymbol OKX 格式：BTC → BTC-USDT。
func okxSymbol(ccy string) string {
	return ccy + "-USDT"
}

// binanceSymbol Binance 格式：BTC → BTCUSDT。
func binanceSymbol(ccy string) string {
	return ccy + "USDT"
}

// CalcSpreads 从公开行情算 OKX vs Binance spot 价差。
// OKX：GET https://www.okx.com/api/v5/market/ticker?instId={okxSymbol}（公开），取 data[0].last
// Binance：GET {bnAPIURL}/v3/ticker/price?symbol={binanceSymbol}（走代理 /api，公开），取 price
// spread_bps = (high-low)/low * 10000
// venue_buy=低价所，venue_sell=高价所
// 单币种错误跳过（log warn 继续）。
func CalcSpreads(ctx context.Context, client *http.Client, bnAPIURL string, symbols []string) ([]SpreadResult, error) {
	var out []SpreadResult
	for _, ccy := range symbols {
		okxPrice, err := fetchOKXTicker(ctx, client, "https://www.okx.com", ccy)
		if err != nil {
			log.Printf("[cexscan] okx ticker %s: %v", ccy, err)
			continue
		}
		bnPrice, err := fetchBinanceTicker(ctx, client, bnAPIURL, ccy)
		if err != nil {
			log.Printf("[cexscan] binance ticker %s: %v", ccy, err)
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

// fetchOKXTicker 取 OKX 最新成交价（公开端点）。
func fetchOKXTicker(ctx context.Context, client *http.Client, base, ccy string) (float64, error) {
	url := fmt.Sprintf("%s/api/v5/market/ticker?instId=%s", base, okxSymbol(ccy))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("okx ticker request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("okx ticker HTTP %d: %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, fmt.Errorf("okx ticker decode: %w", err)
	}
	if raw.Code != "0" || len(raw.Data) == 0 {
		return 0, fmt.Errorf("okx ticker code %s msg %s", raw.Code, raw.Msg)
	}
	return strconv.ParseFloat(raw.Data[0].Last, 64)
}

// fetchBinanceTicker 取 Binance 最新价格（公开端点，走代理 /api）。
func fetchBinanceTicker(ctx context.Context, client *http.Client, base, ccy string) (float64, error) {
	url := fmt.Sprintf("%s/v3/ticker/price?symbol=%s", base, binanceSymbol(ccy))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("binance ticker request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("binance ticker HTTP %d: %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, fmt.Errorf("binance ticker decode: %w", err)
	}
	return strconv.ParseFloat(raw.Price, 64)
}
