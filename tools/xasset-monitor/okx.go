// okx.go D-423：OKX 公开行情路由（自 main.go 拆出保行预算）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// okxTickerResp okx /api/v5/market/ticker 响应（instId 永续）；fundingRate 同源
// funding-rate 端点 data[0]（periods 由 fundingTime/nextFundingTime 间隔实算）。
type okxTickerResp struct {
	Data []struct {
		BidPx string `json:"bidPx"`
		AskPx string `json:"askPx"`
	} `json:"data"`
}

type okxFundingResp struct {
	Data []struct {
		FundingRate     string `json:"fundingRate"`
		FundingTime     string `json:"fundingTime"`
		NextFundingTime string `json:"nextFundingTime"`
	} `json:"data"`
}

// fetchOkxTicker D-423：OKX 公开行情路由——GET /api/v5/market/ticker?instId= 取 bid/ask，
// GET /api/v5/public/funding-rate?instId= 取 fundingRate + 结算间隔实算 periods/day。
// 公共端点无鉴权不走 cex-proxy（SSOT：跨市场同品种簿隔离）。
func fetchOkxTicker(ctx context.Context, hc *http.Client, instID string) (bid, ask, fundingRate float64, periodsPerDay int, err error) {
	const base = "https://www.okx.com"
	var tr okxTickerResp
	if err := fetchOkxJSON(ctx, hc, base+"/api/v5/market/ticker?instId="+instID, &tr); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("ticker: %w", err)
	}
	if len(tr.Data) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("ticker %s: empty data", instID)
	}
	if bid, err = strconv.ParseFloat(tr.Data[0].BidPx, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse bidPx: %w", err)
	}
	if ask, err = strconv.ParseFloat(tr.Data[0].AskPx, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse askPx: %w", err)
	}
	var fr okxFundingResp
	if err := fetchOkxJSON(ctx, hc, base+"/api/v5/public/funding-rate?instId="+instID, &fr); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("funding-rate: %w", err)
	}
	if len(fr.Data) == 0 {
		return 0, 0, 0, 0, fmt.Errorf("funding-rate %s: empty data", instID)
	}
	if fundingRate, err = strconv.ParseFloat(fr.Data[0].FundingRate, 64); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse fundingRate: %w", err)
	}
	// 结算间隔实算：nextFundingTime−fundingTime（ms）→ periods/day；缺/异常→3（8h 常规，warn 由调用方链不吞错）。
	periodsPerDay = 3
	if ft, e1 := strconv.ParseInt(fr.Data[0].FundingTime, 10, 64); e1 == nil {
		if nt, e2 := strconv.ParseInt(fr.Data[0].NextFundingTime, 10, 64); e2 == nil && nt > ft {
			if ih := (nt - ft) / (3600 * 1000); ih > 0 {
				periodsPerDay = int(24 / ih)
			}
		}
	}
	return bid, ask, fundingRate, periodsPerDay, nil
}

// fetchOkxJSON 公开端点 GET+JSON 解码（binance fetchBinanceJSON 同构但 URL 为公网全地址）。
func fetchOkxJSON(ctx context.Context, hc *http.Client, url string, v any) error {
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
