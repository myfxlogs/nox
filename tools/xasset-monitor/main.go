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
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"

	"arb/internal/httpx" // SSOT：自动注入 CA + X-Proxy-Token
	"arb/internal/notify"
	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
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
	pool := st.Pool()

	hc := httpx.Client()

	// 读 Exness XAU tick 最新行。
	exBid, exAsk, err := latestExnessXAU(ctx, pool, exnessVenue)
	if err != nil {
		return fmt.Errorf("read exness tick: %w", err)
	}

	// 9-16 业主并存裁定：PAXG+XAUT 两对逐对 fail-open（一对失败不拖累另一对，同 S2 funding 口径）。
	var firstErr error
	for _, p := range []struct{ sym, pair string }{{"PAXGUSDT", "PAXG-XAU"}, {"XAUTUSDT", "XAUT-XAU"}} {
		if err := runPair(ctx, st, hc, proxyURL, p.sym, p.pair, exBid, exAsk); err != nil {
			log.Printf("xasset-monitor: pair=%s 失败: %v", p.pair, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func runPair(ctx context.Context, st *pgstore.Store, hc *http.Client, proxyURL, sym, pair string, exBid, exAsk float64) error {
	bid, ask, fundingRate, err := fetchBinanceGold(ctx, hc, proxyURL, sym)
	if err != nil {
		return fmt.Errorf("fetch binance %s: %w", sym, err)
	}

	// 算指标（口径分离 D1）。
	entrySpread := (bid - exAsk) / exAsk * 10000
	exitSpread := (ask - exBid) / exBid * 10000
	const binanceMakerCostBps = 4.0 // 0.04% × 2 开+平
	exnessSpread := (exAsk - exBid) / exAsk * 10000
	entryNet := entrySpread - binanceMakerCostBps - exnessSpread
	fundingAnnualPct := fundingRate * 3 * 365 * 100
	tokenMid := (bid + ask) / 2
	xauMid := (exBid + exAsk) / 2
	trackingErr := math.Abs(tokenMid-xauMid) / xauMid * 10000
	equivAnnual := entryNet/100 + fundingAnnualPct

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

func fetchBinanceGold(ctx context.Context, hc *http.Client, proxyURL, symbol string) (bid, ask, fundingRate float64, err error) {
	bt, err := fetchJSON(ctx, hc, proxyURL+"/fapi/v1/ticker/bookTicker?symbol="+symbol)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("bookTicker: %w", err)
	}
	bid, err = strconv.ParseFloat(bt.BidPrice, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse bidPrice: %w", err)
	}
	ask, err = strconv.ParseFloat(bt.AskPrice, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse askPrice: %w", err)
	}
	pi, err := fetchJSONPremium(ctx, hc, proxyURL+"/fapi/v1/premiumIndex?symbol="+symbol)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("premiumIndex: %w", err)
	}
	fundingRate, err = strconv.ParseFloat(pi.LastFundingRate, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse lastFundingRate: %w", err)
	}
	return bid, ask, fundingRate, nil
}

func fetchJSON(ctx context.Context, hc *http.Client, url string) (*binanceBookTicker, error) {
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
	var bt binanceBookTicker
	if err := json.NewDecoder(resp.Body).Decode(&bt); err != nil {
		return nil, err
	}
	return &bt, nil
}

func fetchJSONPremium(ctx context.Context, hc *http.Client, url string) (*binancePremiumIndex, error) {
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
	var pi binancePremiumIndex
	if err := json.NewDecoder(resp.Body).Decode(&pi); err != nil {
		return nil, err
	}
	return &pi, nil
}

func latestExnessXAU(ctx context.Context, pool *pgxpool.Pool, venue string) (bid, ask float64, err error) {
	err = pool.QueryRow(ctx, `
		SELECT bid, ask FROM ticks WHERE venue=$1 AND symbol='XAUUSD' ORDER BY ts DESC LIMIT 1
	`, venue).Scan(&bid, &ask)
	return bid, ask, err
}

// alertItem 告警项。
type alertItem struct {
	name string
	val  float64
	thr  float64
	op   string // "<" 或 ">"
}

func checkAlerts(entrySpread, exitSpread, entryNet, fundingAnnual, fundingRate, trackingErr, equivAnnual float64) []alertItem {
	var out []alertItem
	if entrySpread < 10 {
		out = append(out, alertItem{"进场价差不足", entrySpread, 10, "<"})
	}
	if exitSpread > 10 {
		out = append(out, alertItem{"平仓成本高", exitSpread, 10, ">"})
	}
	if entryNet < 5 {
		out = append(out, alertItem{"进场无利", entryNet, 5, "<"})
	}
	if fundingAnnual < 1.0 {
		out = append(out, alertItem{"funding 年化过低", fundingAnnual, 1.0, "<"})
	}
	if fundingRate < 0 {
		out = append(out, alertItem{"funding 单次负值", fundingRate, 0, "<"})
	}
	if trackingErr > 50 {
		out = append(out, alertItem{"跟踪误差过大", trackingErr, 50, ">"})
	}
	if equivAnnual < 2.0 {
		out = append(out, alertItem{"等效年化低于基准", equivAnnual, 2.0, "<"})
	}
	return out
}

func sendFeishuAlert(ctx context.Context, st *pgstore.Store, alerts []alertItem, rec *pgstore.XassetMonitor) {
	appID := os.Getenv("ARB_FEISHU_APP_ID")
	appSecret := os.Getenv("ARB_FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		log.Printf("xasset-monitor: 飞书凭据未配置，告警跳过（%d 项）", len(alerts))
		return
	}
	fc := notify.NewFeishuClient(appID, appSecret)
	if !fc.Enabled() {
		log.Printf("xasset-monitor: 飞书客户端未启用，告警跳过（%d 项）", len(alerts))
		return
	}
	chatID, err := st.FeishuChatID(ctx)
	if err != nil {
		log.Printf("xasset-monitor: 读 chat_id 失败: %v", err)
		return
	}
	if chatID == "" {
		log.Printf("xasset-monitor: chat_id 未绑定，告警跳过（%d 项）", len(alerts))
		return
	}
	text := fmt.Sprintf("[%s 监控] %d 项告警\n", rec.Pair, len(alerts))
	for _, a := range alerts {
		text += fmt.Sprintf("• %s: %.4f (阈值 %s %.4f)\n", a.name, a.val, a.op, a.thr)
	}
	text += fmt.Sprintf("\n当前：进场价差 %.2fbps / 进场净收益 %.2fbps / funding 年化 %.2f%% / 跟踪误差 %.2fbps / 等效年化 %.2f%%",
		rec.EntrySpreadBps, rec.EntryNetBps, rec.FundingAnnualPct, rec.TrackingErrorBps, rec.EquivAnnualPct)
	if err := fc.SendText(ctx, chatID, text); err != nil {
		log.Printf("xasset-monitor: 飞书告警发送失败: %v", err)
		return
	}
	log.Printf("xasset-monitor: 飞书告警已推送（%d 项）", len(alerts))
}
