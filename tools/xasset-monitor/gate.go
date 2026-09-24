// gate.go D-462：Gate futures 公开行情路由（api.gateio.ws 无鉴权不走 cex-proxy，OKX 同构）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// gateTickerResp gate /api/v4/futures/usdt/tickers 数组响应——9-24 实测 XAU_USDT 含
// highest_bid/lowest_ask/funding_rate（预测利率）；字段在单端点直用，无需 order_book 兜底。
type gateTickerResp []struct {
	HighestBid string `json:"highest_bid"`
	LowestAsk  string `json:"lowest_ask"`
}

// gateContractResp /api/v4/futures/usdt/contracts/<C>——funding_interval 实算 periods/day。
type gateContractResp struct {
	FundingRate     string `json:"funding_rate"`
	FundingInterval int64  `json:"funding_interval"`
}

// gateAPIBase 测试可覆写 seam（生产=api.gateio.ws 公开端点）。
var gateAPIBase = "https://api.gateio.ws"

// fetchGateTicker Gate 公开行情路由：tickers?contract=<C> 取 bid/ask（XAU_USDT 风格），
// contracts/<C> 取 funding_rate + funding_interval（秒）实算 periods/day；interval≤0→err（D-079）。
func fetchGateTicker(ctx context.Context, hc *http.Client, contract string) (bid, ask, fundingRate float64, periodsPerDay int, err error) {
	base := gateAPIBase
	var tr gateTickerResp
	if err := fetchGateJSON(ctx, hc, base+"/api/v4/futures/usdt/tickers?contract="+contract, &tr); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("ticker: %w", err)
	}
	if len(tr) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("ticker %s: empty array", contract)
	}
	if bid, err = strconv.ParseFloat(tr[0].HighestBid, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse highest_bid: %w", err)
	}
	if ask, err = strconv.ParseFloat(tr[0].LowestAsk, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse lowest_ask: %w", err)
	}
	var cr gateContractResp
	if err := fetchGateJSON(ctx, hc, base+"/api/v4/futures/usdt/contracts/"+contract, &cr); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("contract: %w", err)
	}
	if fundingRate, err = strconv.ParseFloat(cr.FundingRate, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse funding_rate: %w", err)
	}
	if cr.FundingInterval <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("contract %s: funding_interval=%d", contract, cr.FundingInterval)
	}
	periodsPerDay = int(86400 / cr.FundingInterval)
	return bid, ask, fundingRate, periodsPerDay, nil
}

// fetchGateJSON 公开端点 GET+JSON 解码（fetchOkxJSON 同构）。
func fetchGateJSON(ctx context.Context, hc *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
