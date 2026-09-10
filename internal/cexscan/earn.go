// D-271 S2 理财 APR 采集器：OKX Savings + Binance Simple Earn Flexible。
package cexscan

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/config"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

// EarnCollector 采集 OKX + Binance 理财 APR。
type EarnCollector struct {
	client    *http.Client
	okxKey    string
	okxSecret string
	okxPass   string
	bnKey     string
	bnSecret  string
	bnSapiURL string // Binance sapi base（从 base_url /api 或 /fapi 替换为 /sapi）
}

// NewEarnCollector 从 exchange_configs 筛 simulated=false 行取凭据。
// Binance sapi URL = base_url 中 /api 或 /fapi 替换为 /sapi。
func NewEarnCollector(cfg *config.Config, exchs []pgstore.ExchangeConfig) *EarnCollector {
	ec := &EarnCollector{client: httpx.Client()}
	for _, e := range exchs {
		if e.Simulated {
			continue
		}
		switch e.Venue {
		case "okx":
			if e.VenueType == "spot" { // OKX 只需一组凭据，取 spot 行
				ec.okxKey = e.APIKey
				ec.okxSecret = e.APISecret
				ec.okxPass = e.Passphrase
			}
		case "binance":
			if e.VenueType == "spot" { // Binance 只需一组凭据，取 spot 行
				ec.bnKey = e.APIKey
				ec.bnSecret = e.APISecret
				ec.bnSapiURL = strings.Replace(e.BaseURL, "/api", "/sapi", 1)
				ec.bnSapiURL = strings.Replace(ec.bnSapiURL, "/fapi", "/sapi", 1)
			}
		}
	}
	return ec
}

// CollectOKX 采集 OKX Savings lending-rate-summary。
// 端点：GET https://www.okx.com/api/v5/finance/savings/lending-rate-summary
// 解析：data[].ccy + data[].estRate（小数年化，0.035=3.5%）
func (c *EarnCollector) CollectOKX(ctx context.Context) ([]pgstore.CexEarnAPR, error) {
	return collectOKXWithBase(ctx, c, "https://www.okx.com")
}

// collectOKXWithBase 可注入 base URL 的 CollectOKX（测试用）。
func collectOKXWithBase(ctx context.Context, c *EarnCollector, base string) ([]pgstore.CexEarnAPR, error) {
	if c.okxKey == "" {
		return nil, fmt.Errorf("okx key not configured")
	}
	const path = "/api/v5/finance/savings/lending-rate-summary"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	prehash := ts + "GET" + path
	sign := okxSign(c.okxSecret, prehash)

	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("OK-ACCESS-KEY", c.okxKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.okxPass)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("okx earn request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("okx earn HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Ccy     string `json:"ccy"`
			EstRate string `json:"estRate"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("okx earn decode: %w", err)
	}
	if raw.Code != "0" {
		return nil, fmt.Errorf("okx earn code %s: %s", raw.Code, raw.Msg)
	}

	var out []pgstore.CexEarnAPR
	for _, d := range raw.Data {
		rate, err := strconv.ParseFloat(d.EstRate, 64)
		if err != nil {
			log.Printf("[cexscan] okx estRate parse %q: %v", d.EstRate, err)
			continue
		}
		out = append(out, pgstore.CexEarnAPR{
			Venue:       "okx",
			Ccy:         d.Ccy,
			ProductType: "flexible",
			APR:         rate,
		})
	}
	return out, nil
}

// CollectBinance 采集 Binance Simple Earn Flexible。
// 端点：GET {bnSapiURL}/v1/simple-earn/flexible/list?timestamp={ms}&recvWindow=5000&signature={sign}
// 解析：rows[].asset + rows[].latestAnnualPercentageRate（小数年化）+ rows[].tierAnnualPercentageRate（JSON object）
func (c *EarnCollector) CollectBinance(ctx context.Context) ([]pgstore.CexEarnAPR, error) {
	if c.bnKey == "" {
		return nil, fmt.Errorf("binance key not configured")
	}
	ms := time.Now().UnixMilli()
	query := fmt.Sprintf("timestamp=%d&recvWindow=5000", ms)
	sign := binanceSign(c.bnSecret, query)
	url := fmt.Sprintf("%s/v1/simple-earn/flexible/list?%s&signature=%s", c.bnSapiURL, query, sign)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", c.bnKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance earn request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("binance earn HTTP %d: %s", resp.StatusCode, string(body))
	}

	var rows []struct {
		Asset                      string          `json:"asset"`
		LatestAnnualPercentageRate string          `json:"latestAnnualPercentageRate"`
		TierAnnualPercentageRate   json.RawMessage `json:"tierAnnualPercentageRate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("binance earn decode: %w", err)
	}

	var out []pgstore.CexEarnAPR
	for _, r := range rows {
		rate, err := strconv.ParseFloat(r.LatestAnnualPercentageRate, 64)
		if err != nil {
			log.Printf("[cexscan] binance rate parse %q: %v", r.LatestAnnualPercentageRate, err)
			continue
		}
		var tierAPR string
		if len(r.TierAnnualPercentageRate) > 0 && string(r.TierAnnualPercentageRate) != "null" {
			tierAPR = string(r.TierAnnualPercentageRate)
		}
		row := pgstore.CexEarnAPR{
			Venue:       "binance",
			Ccy:         r.Asset,
			ProductType: "flexible",
			APR:         rate,
		}
		if tierAPR != "" {
			row.TierAPR = sql.NullString{String: tierAPR, Valid: true}
		}
		out = append(out, row)
	}
	return out, nil
}

// Collect 合并 OKX+Binance，忽略单所错误（log warn 继续）。
func (c *EarnCollector) Collect(ctx context.Context) ([]pgstore.CexEarnAPR, error) {
	var out []pgstore.CexEarnAPR
	if aprs, err := c.CollectOKX(ctx); err != nil {
		log.Printf("[cexscan] okx earn collect: %v", err)
	} else {
		out = append(out, aprs...)
	}
	if aprs, err := c.CollectBinance(ctx); err != nil {
		log.Printf("[cexscan] binance earn collect: %v", err)
	} else {
		out = append(out, aprs...)
	}
	return out, nil
}
