// derive.go —— D-479 S1：自 main.go 整段搬移（纯搬移零逻辑变更）——
// 派生簿入参查询（loadDerivationInputs）+ binance/exness 数据拉取层（类型/seam/venue fetch）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

// loadDerivationInputs D-472 S5：派生入参两查询（enabled 组合行+TRADING universe 行）。
func loadDerivationInputs(ctx context.Context, st *pgstore.Store) ([]allowComboRow, []universeRow, error) {
	var combos []allowComboRow
	// D-552 S1：SELECT venue_key_* 生成列（D-496 键域权威口径）——裸列 venue_buy/sell
	// 在拆列后=裸号/裸名（"3001157"/"gate"），IsMTKey 全 false → mt×cex 组合误判同所
	// 拒采 → monitor 簿停写 49h（回归实证见 derive_test T3）。
	rows, err := st.Pool().Query(ctx, `SELECT canonical_buy, venue_key_buy, canonical_sell, venue_key_sell
		FROM xasset_allow_combos WHERE enabled = true ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("list combos: %w", err)
	}
	for rows.Next() {
		var c allowComboRow
		if err := rows.Scan(&c.canonicalBuy, &c.venueBuy, &c.canonicalSell, &c.venueSell); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan combo: %w", err)
		}
		combos = append(combos, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("combos rows: %w", err)
	}

	var uni []universeRow
	uniRows, err := st.Pool().Query(ctx, `SELECT venue, symbol, hedge_canonical
		FROM fin_cex_universe WHERE status = 'TRADING'`)
	if err != nil {
		return nil, nil, fmt.Errorf("list universe: %w", err)
	}
	for uniRows.Next() {
		var r universeRow
		if err := uniRows.Scan(&r.venue, &r.symbol, &r.hedgeCanonical); err != nil {
			uniRows.Close()
			return nil, nil, fmt.Errorf("scan universe: %w", err)
		}
		uni = append(uni, r)
	}
	uniRows.Close()
	return combos, uni, uniRows.Err()
}

// binanceBookTicker fapi/v1/ticker/bookTicker 响应。
type binanceBookTicker struct {
	BidPrice string `json:"bidPrice"`
	AskPrice string `json:"askPrice"`
}

// binancePremiumIndex fapi/v1/premiumIndex 响应。
type binancePremiumIndex struct {
	LastFundingRate string `json:"lastFundingRate"`
}

func fetchBinanceGold(ctx context.Context, hc *http.Client, proxyURL, symbol string) (bid, ask, fundingRate float64, err error) {
	bt, err := fetchBinanceJSON(ctx, hc, proxyURL+"/fapi/v1/ticker/bookTicker?symbol="+symbol, func() any { return &binanceBookTicker{} })
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bookTicker: %w", err)
	}
	bid, err = strconv.ParseFloat(bt.(*binanceBookTicker).BidPrice, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse bidPrice: %w", err)
	}
	ask, err = strconv.ParseFloat(bt.(*binanceBookTicker).AskPrice, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse askPrice: %w", err)
	}
	pi, err := fetchBinanceJSON(ctx, hc, proxyURL+"/fapi/v1/premiumIndex?symbol="+symbol, func() any { return &binancePremiumIndex{} })
	if err != nil {
		return 0, 0, 0, fmt.Errorf("premiumIndex: %w", err)
	}
	fundingRate, err = strconv.ParseFloat(pi.(*binancePremiumIndex).LastFundingRate, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse lastFundingRate: %w", err)
	}
	return bid, ask, fundingRate, nil
}

// fetchBinanceJSON 通用 GET+JSON 解码（合并 fetchJSON/fetchJSONPremium，D-301a 行数治理）。
func fetchBinanceJSON(ctx context.Context, hc *http.Client, url string, mk func() any) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	v := mk()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return nil, err
	}
	return v, nil
}

// latestExnessTick D-335R1 S1：读 Exness 指定符号最新 tick（XAUUSD 泛化参数化）。
// 行龄 >10min 或无行 → error（调用方该对 skip+warn，禁止顶替腿）。
func latestExnessTick(ctx context.Context, pool *pgxpool.Pool, venue, symbol string) (bid, ask float64, err error) {
	var ts time.Time
	err = pool.QueryRow(ctx, `
		SELECT bid, ask, ts FROM ticks WHERE venue=$1 AND symbol=$2 ORDER BY ts DESC LIMIT 1
	`, venue, symbol).Scan(&bid, &ask, &ts)
	if err != nil {
		return 0, 0, fmt.Errorf("latest tick: %w", err)
	}
	if time.Since(ts) > 10*time.Minute {
		return 0, 0, fmt.Errorf("stale tick age=%v > 10m", time.Since(ts).Round(time.Second))
	}
	return bid, ask, nil
}

// fetchFundingPeriodsPerDay D-301a S2：从 Binance fundingInfo 取 per-symbol 结算周期。
// 返回 periods/day（4h=6, 8h=3）；失败 → error（调用方回退 3）。
func fetchFundingPeriodsPerDay(ctx context.Context, hc *http.Client, proxyURL, symbol string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL+"/fapi/v1/fundingInfo", nil)
	if err != nil {
		return 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("fundingInfo http %d", resp.StatusCode)
	}
	var fi []struct {
		Symbol               string `json:"symbol"`
		FundingIntervalHours any    `json:"fundingIntervalHours"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fi); err != nil {
		return 0, err
	}
	for _, f := range fi {
		if f.Symbol != symbol {
			continue
		}
		hours := 0
		switch v := f.FundingIntervalHours.(type) {
		case float64:
			hours = int(v)
		case string:
			hours, _ = strconv.Atoi(v)
		}
		if hours <= 0 {
			return 0, fmt.Errorf("fundingInfo %s: invalid hours %v", symbol, f.FundingIntervalHours)
		}
		return 24 / hours, nil
	}
	return 0, fmt.Errorf("fundingInfo: symbol %s not found", symbol)
}
