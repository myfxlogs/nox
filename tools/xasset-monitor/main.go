// D-299 PAXG-XAU 跨市场套利监控脚本（独立工具，cron 调度）。
// 9-16 业主并存裁定：PAXG+XAUT 两对都监控（XAUTUSDT=Tether Gold 永续，funding 当前更高）。
//
// 流程：拉 Binance 黄金代币合约 bid/ask + funding rate（走 cex-proxy，httpx.Client 自动注入 CA+token）
// → 读 Exness XAU tick 最新行 → 算价差/净收益/跟踪误差 → 入库 fin_xasset_monitor
// → 超阈值飞书告警。不自动下单，只监控+纸面交易支撑。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"arb/internal/httpx" // SSOT：自动注入 CA + X-Proxy-Token
	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	backfill := flag.Bool("backfill", false, "D-341 S5：价差史深回填（ticks×klines 5min 对齐，src=backfill）")
	flag.Parse()
	if *backfill {
		if err := runBackfillMode(ctx); err != nil {
			log.Fatalf("xasset-monitor backfill: %v", err)
		}
		return
	}
	if err := run(ctx); err != nil {
		log.Fatalf("xasset-monitor: %v", err)
	}
}

func run(ctx context.Context) error {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return fmt.Errorf("DB_DSN 未设置")
	}
	proxyURL := os.Getenv("BINANCE_PROXY_URL")
	if proxyURL == "" {
		return fmt.Errorf("BINANCE_PROXY_URL 未设置")
	}
	exnessVenue := os.Getenv("ARB_XASSET_EXNESS_VENUE")
	if exnessVenue == "" {
		exnessVenue = "3001157"
	}

	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer st.Close()

	hc := httpx.Client()

	// D-472 S5：监控簿白名单派生（硬编码清单退役）——enabled combos × universe 现查。
	combos, uni, err := loadDerivationInputs(ctx, st)
	if err != nil {
		return fmt.Errorf("monitor pairs inputs: %w", err)
	}
	pairs, uncovered := deriveMonitorPairs(combos, uni)
	for _, u := range uncovered {
		log.Printf("[monitor-pairs] warn: enabled 组合未被簿覆盖（一次性列名，B-008）: %s", u)
	}
	return runPairs(ctx, st, hc, proxyURL, exnessVenue, pairs)
}

// loadDerivationInputs D-472 S5：派生入参两查询（enabled 组合行+TRADING universe 行）。
func loadDerivationInputs(ctx context.Context, st *pgstore.Store) ([]allowComboRow, []universeRow, error) {
	var combos []allowComboRow
	rows, err := st.Pool().Query(ctx, `SELECT canonical_buy, venue_buy, canonical_sell, venue_sell
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

// runPairs S2：逐对取各自 exSym 报价（无新鲜 tick → 该对 skip+warn 逐对 fail-open，
// 9-16 业主并存裁定）——禁止任何腿顶替。返回首个 pair 失败错误（skip 非失败）。
// D-423：mtVenueKey=exnessVenue+:mt5（已带冒号原样）；按 cexVenue 路由报价源。
func runPairs(ctx context.Context, st *pgstore.Store, hc *http.Client, proxyURL, exnessVenue string, pairs []monitorPair) error {
	mtVenue := exnessVenue
	if !strings.Contains(mtVenue, ":") {
		mtVenue += ":mt5"
	}
	var firstErr error
	for _, p := range pairs {
		exBid, exAsk, err := latestExnessTick(ctx, st.Pool(), exnessVenue, p.exSym)
		if err != nil {
			log.Printf("xasset-monitor: pair=%s skip (Exness 腿 %s 无新鲜 tick: %v)", p.pair, p.exSym, err)
			continue // S2：逐对 skip，禁止顶替腿
		}
		if err := runPair(ctx, st, hc, proxyURL, p, mtVenue, exBid, exAsk); err != nil {
			log.Printf("xasset-monitor: pair=%s 失败: %v", p.pair, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func runPair(ctx context.Context, st *pgstore.Store, hc *http.Client, proxyURL string, p monitorPair, mtVenue string, exBid, exAsk float64) error {
	sym, pair := p.sym, p.pair
	var bid, ask, fundingRate float64
	var periodsPerDay int
	var err error
	if p.cexVenue == "okx:perp" {
		// D-423：OKX 公开行情路由（api/v5/market/ticker + public/funding-rate，无鉴权不走 proxy）。
		bid, ask, fundingRate, periodsPerDay, err = fetchOkxTickerFn(ctx, hc, sym)
		if err != nil {
			return fmt.Errorf("fetch okx %s: %w", sym, err)
		}
	} else if p.cexVenue == "gate:perp" {
		// D-462：Gate 公开行情路由（api/v4/futures/usdt tickers+contracts，无鉴权不走 proxy）。
		bid, ask, fundingRate, periodsPerDay, err = fetchGateTickerFn(ctx, hc, sym)
		if err != nil {
			return fmt.Errorf("fetch gate %s: %w", sym, err)
		}
	} else {
		bid, ask, fundingRate, err = fetchBinanceGoldFn(ctx, hc, proxyURL, sym)
		if err != nil {
			return fmt.Errorf("fetch binance %s: %w", sym, err)
		}
		// D-301a S2：funding 年化周期按 symbol 取真值（fundingInfo）——PAXG/XAUT 实为 4h→6 期/日，旧版 ×3 低估 2×。
		periodsPerDay, err = fetchFundingPeriodsPerDayFn(ctx, hc, proxyURL, sym)
		if err != nil {
			log.Printf("xasset-monitor: pair=%s fundingInfo fetch failed, fallback 3/day: %v", pair, err)
			periodsPerDay = 3 // D-079 禁静默回退
		}
	}

	// 算指标（口径分离 D1）。
	entrySpread := (bid - exAsk) / exAsk * 10000
	exitSpread := (ask - exBid) / exBid * 10000
	// D-462：三 venue VIP0 maker 费率同为 0.02%×2=4bps（binance 单 C 档；gate fee 表
	// gate.com/announcements/article/101365 VIP0 maker 0.0200%）→ 常量化共享。
	const cexMakerCostBps = 4.0
	exnessSpread := (exAsk - exBid) / exAsk * 10000
	entryNet := entrySpread - cexMakerCostBps - exnessSpread // 开+平净价差（maker 0.04%×2+Exness 点差）
	fundingAnnualPct := fundingRate * float64(periodsPerDay) * 365 * 100
	tokenMid := (bid + ask) / 2
	xauMid := (exBid + exAsk) / 2
	trackingErr := math.Abs(tokenMid-xauMid) / xauMid * 10000
	equivAnnual := entryNet/100 + fundingAnnualPct

	// D-335 S1：noise_bps=trailing 24h 摆幅半径 (max−min)/2（含当前行；样本 <12 → NULL）。
	// D-423：样本按 (pair,cex_venue,mt_venue) 三元组隔离——跨 venue 簿统计=借用伪半径。
	noiseBps := computeNoiseBps(append(trailing24hEntries(ctx, st.Pool(), pair, p.cexVenue, mtVenue), entrySpread))
	// D-341 S1：harvest_floor=min(q25, entry−MIN_CAPTURE)——trailing 全深序列（含当前行）。
	harvestFloor := computeHarvestFloor(append(trailingAllEntries(ctx, st.Pool(), pair, p.cexVenue, mtVenue), entrySpread), entrySpread)

	rec := &pgstore.XassetMonitor{
		Pair:             pair,
		BinanceBid:       bid,
		BinanceAsk:       ask,
		ExnessBid:        exBid,
		ExnessAsk:        exAsk,
		EntrySpreadBps:   entrySpread,
		ExitSpreadBps:    exitSpread,
		EntryNetBps:      entryNet,
		FundingRate8h:    fundingRate,
		FundingAnnualPct: fundingAnnualPct,
		TrackingErrorBps: trackingErr,
		EquivAnnualPct:   equivAnnual,
		NoiseBps:         noiseBps,
		HarvestFloorBps:  harvestFloor,
		Src:              "live",
		CexVenue:         p.cexVenue,
		MtVenue:          mtVenue,
	}
	if err := st.InsertXassetMonitor(ctx, rec); err != nil {
		return fmt.Errorf("insert monitor: %w", err)
	}
	log.Printf("xasset-monitor: pair=%s entry_spread=%.2fbps entry_net=%.2fbps funding_annual=%.2f%% tracking_err=%.2fbps equiv_annual=%.2f%%",
		rec.Pair, entrySpread, entryNet, fundingAnnualPct, trackingErr, equivAnnual)

	// 告警判断（任一触发即推飞书）。
	alerts := checkAlerts(entrySpread, exitSpread, entryNet, fundingAnnualPct, fundingRate, trackingErr, equivAnnual)
	if len(alerts) > 0 {
		sendFeishuAlert(ctx, st, alerts, rec)
	}
	return nil
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

// fetchBinanceGoldFn/fetchFundingPeriodsPerDayFn/fetchOkxTickerFn 测试 seam
// （生产=真实现；测试 stub 隔离 cex-proxy/公网）。
var (
	fetchBinanceGoldFn          = fetchBinanceGold
	fetchFundingPeriodsPerDayFn = fetchFundingPeriodsPerDay
	fetchOkxTickerFn            = fetchOkxTicker
	fetchGateTickerFn           = fetchGateTicker
)

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
