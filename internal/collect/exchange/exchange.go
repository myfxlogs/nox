// Package exchange 实现 CEX 行情采集（§3.3）。
// 当前阶段实现 REST 轮询 funding + ticker（后续迭代升级 WS）。
// 产出 Fact（funding）+ Quote（ticker），分别经 FactSink 和 QuoteBus。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// Venue 常量。
const (
	VenueBinance = "binance"
	VenueGate    = "gate"
	VenueOKX     = "okx"
)

// fundingPeriodsPerDay 根据结算间隔（ms）计算每日结算次数。
// 4h=6, 1h=24, 8h=3。intervalMs ≤ 0 → 回退 3（8h，禁静默，调用方计数日志）。
//
// D-079 修复：旧版用「nextFunding - now」（剩余时间）当间隔 → 结算前 1h 内剩余极短
// → periodsPerDay 爆炸到 26 → 年化虚高 8.7 倍（94.9% vs 真实 10.95%）。
// 正确做法：用「nextFunding - prevFunding」（结算间隔），与当前时刻无关。
func fundingPeriodsPerDay(intervalMs float64) int {
	if intervalMs <= 0 {
		return 3 // 回退 8h×3（禁静默，调用方计数日志）
	}
	periodsPerDay := float64(24*3600*1000) / intervalMs
	if periodsPerDay < 1 {
		return 3
	}
	return int(periodsPerDay)
}

// annualizeFunding 将单次 funding rate 转为年化百分比（D-077 修复⑪B N1）。
// 公式：rate × periodsPerDay × 365 × 100
func annualizeFunding(rate float64, periodsPerDay int) float64 {
	return rate * float64(periodsPerDay) * 365 * 100
}

// Config CEX 采集配置。
type Config struct {
	Symbols        []string       // 基础币种 BTC/ETH/TRX
	SymProvider    SymbolProvider // D-369 S2：动态符号集（radar_watchlist ∪ env；nil=仅 env）
	BinanceBaseURL string         // Binance fapi base，可指向 CF Worker 代理
	GateBaseURL    string
	OKXBaseURL     string
	Client         *http.Client
}

// SymbolProvider D-369 S2：动态符号集供给（radar_watchlist provider 实现；nil 安全）。
type SymbolProvider interface {
	Symbols() []string
}

// effectiveSymbols env 基础集 ∪ provider 动态集（去重；provider nil/空集 → env 原样）。
func (c Config) effectiveSymbols() []string {
	if c.SymProvider == nil {
		return c.Symbols
	}
	extra := c.SymProvider.Symbols()
	if len(extra) == 0 {
		return c.Symbols
	}
	seen := make(map[string]bool, len(c.Symbols)+len(extra))
	out := make([]string, 0, len(c.Symbols)+len(extra))
	for _, s := range c.Symbols {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// DefaultConfig 返回默认配置。
func DefaultConfig(symbols []string) Config {
	return Config{
		Symbols:        symbols,
		BinanceBaseURL: "https://fapi.binance.com",
		GateBaseURL:    "https://api.gateio.ws",
		OKXBaseURL:     "https://www.okx.com",
		Client:         &http.Client{Timeout: 10 * time.Second},
	}
}

// BinanceFunding 采集 Binance funding rate（REST 轮询，§3.3）。
// BinanceBaseURL 可指向 Cloudflare Worker 代理以绕过 451 地区限制。
// D-301a S2：intervals 注入 per-symbol 结算周期真值（fundingInfo fundingIntervalHours），
// nil → 回退 3（8h×3，D-079 禁静默）。
type BinanceFunding struct {
	cfg       Config
	intervals *FundingIntervalCache
}

func NewBinanceFunding(cfg Config) *BinanceFunding { return &BinanceFunding{cfg: cfg} }

// SetFundingIntervals 注入 funding 周期缓存（D-301a S2）。
func (b *BinanceFunding) SetFundingIntervals(c *FundingIntervalCache) { b.intervals = c }

func (b *BinanceFunding) Kind() string { return fact.KindFunding }

func (b *BinanceFunding) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range b.syms() {
		pair := sym + "USDT"
		url := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", b.cfg.BinanceBaseURL, pair)
		body, err := getJSON(ctx, b.cfg.Client, url)
		if err != nil {
			// D-300 S2：单符号失败跳过——新链（PAXG）故障不拖累既有主链。
			fmt.Printf("[exchange:binance] funding %s: %v (skip)\n", pair, err)
			continue
		}
		rate, ok := body["lastFundingRate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-301a S2：per-symbol 结算周期真值（fundingInfo fundingIntervalHours）；
		// D-079：Binance premiumIndex 不返回 prevFundingTime → 旧版固定回退 3/天（8h）。
		// PAXG/XAUT 实为 4h 结算 → 6/天，旧版低估 2×。
		periodsPerDay := fundingPeriodsPerDay(0) // 回退 3/天
		if b.intervals != nil {
			periodsPerDay = b.intervals.PeriodsPerDay(pair)
		}
		annualized := annualizeFunding(f, periodsPerDay)
		out = append(out, fact.Fact{
			Kind:   fact.KindFunding,
			Venue:  VenueBinance,
			Symbol: pair,
			Value:  annualized,
			Unit:   fact.UnitPctAnnualized,
			Ts:     time.Now(),
			Src:    "binance_fapi",
		})
	}
	return out, nil
}

// BinanceTicker 采集 Binance best bid/ask（REST 轮询，§3.3）。
type BinanceTicker struct{ cfg Config }

func NewBinanceTicker(cfg Config) *BinanceTicker { return &BinanceTicker{cfg: cfg} }

func (b *BinanceTicker) PollQuotes(ctx context.Context) ([]bus.Quote, error) {
	var out []bus.Quote
	spotBase := strings.Replace(b.cfg.BinanceBaseURL, "fapi", "api", 1)
	for _, sym := range b.syms() {
		pair := sym + "USDT"
		spotURL := fmt.Sprintf("%s/api/v3/ticker/bookTicker?symbol=%s", spotBase, pair)
		if q, err := fetchBinanceBookTicker(ctx, b.cfg.Client, spotURL, pair, VenueBinance, "spot"); err == nil {
			out = append(out, q)
		}
		perpURL := fmt.Sprintf("%s/fapi/v1/ticker/bookTicker?symbol=%s", b.cfg.BinanceBaseURL, pair)
		if q, err := fetchBinanceBookTicker(ctx, b.cfg.Client, perpURL, pair, VenueBinance, "perp"); err == nil {
			out = append(out, q)
		}
	}
	return out, nil
}

func fetchBinanceBookTicker(ctx context.Context, client *http.Client, url, symbol, venue, venueType string) (bus.Quote, error) {
	body, err := getJSON(ctx, client, url)
	if err != nil {
		return bus.Quote{}, err
	}
	bid, _ := body["bidPrice"].(string)
	ask, _ := body["askPrice"].(string)
	var bidF, askF float64
	fmt.Sscanf(bid, "%f", &bidF)
	fmt.Sscanf(ask, "%f", &askF)
	return bus.Quote{
		Symbol: symbol, Venue: venue, VenueType: venueType, VenueSymbol: symbol,
		Bid: bidF, Ask: askF, Time: time.Now(),
	}, nil
}

// GateFunding 采集 Gate.io funding rate（REST 轮询，§3.3）。
type GateFunding struct {
	cfg Config
}

// NewGateFunding 创建 Gate.io funding 采集器。
func NewGateFunding(cfg Config) *GateFunding { return &GateFunding{cfg: cfg} }

func (g *GateFunding) Kind() string { return fact.KindFunding }

func (g *GateFunding) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range g.syms() {
		contract := sym + "_USDT"
		url := fmt.Sprintf("%s/api/v4/futures/usdt/contracts/%s", g.cfg.GateBaseURL, contract)
		body, err := getJSON(ctx, g.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:gate] funding %s: %v (skip)\n", contract, err)
			continue
		}
		rate, ok := body["funding_rate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-301a S2：Gate contracts 接口返回 funding_interval（秒）→ per-symbol 真值；
		// D-079：旧版固定回退 3/天（8h），4h 结算币种低估 2×。
		var intervalSec float64
		if v, ok := body["funding_interval"].(float64); ok {
			intervalSec = v
		}
		periodsPerDay := fundingPeriodsPerDay(intervalSec * 1000) // intervalMs
		annualized := annualizeFunding(f, periodsPerDay)
		out = append(out, fact.Fact{
			Kind:   fact.KindFunding,
			Venue:  VenueGate,
			Symbol: sym + "USDT",
			Value:  annualized,
			Unit:   fact.UnitPctAnnualized,
			Ts:     time.Now(),
			Src:    "gate_v4",
		})
	}
	return out, nil
}

// OKXFunding 采集 OKX funding rate（REST 轮询）。
type OKXFunding struct {
	cfg Config
}

func NewOKXFunding(cfg Config) *OKXFunding { return &OKXFunding{cfg: cfg} }

func (o *OKXFunding) Kind() string { return fact.KindFunding }

func (o *OKXFunding) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range o.syms() {
		instID := sym + "-USDT-SWAP"
		url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", o.cfg.OKXBaseURL, instID)
		body, err := getJSON(ctx, o.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:okx] funding %s: %v (skip)\n", instID, err)
			continue
		}
		data, ok := body["data"].([]any)
		if !ok || len(data) == 0 {
			continue
		}
		item, ok := data[0].(map[string]any)
		if !ok {
			continue
		}
		rate, ok := item["fundingRate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-079：OKX 返回 fundingTime（当前结算）+ nextFundingTime（下次结算）→ 间隔 = 差值。
		// 旧版只用 fundingTime-now（剩余时间）当间隔 → 结算前年化爆炸（94.9% vs 真实 10.95%）。
		var fundingMs, nextFundingMs float64
		if ft, ok := item["fundingTime"].(string); ok {
			fmt.Sscanf(ft, "%f", &fundingMs)
		}
		if nft, ok := item["nextFundingTime"].(string); ok {
			fmt.Sscanf(nft, "%f", &nextFundingMs)
		}
		intervalMs := nextFundingMs - fundingMs
		if intervalMs <= 0 {
			fmt.Printf("[exchange:okx] %s funding interval missing (ft=%v nft=%v), fallback 3/day\n", sym+"USDT", fundingMs, nextFundingMs)
		}
		periodsPerDay := fundingPeriodsPerDay(intervalMs)
		annualized := annualizeFunding(f, periodsPerDay)
		out = append(out, fact.Fact{
			Kind:   fact.KindFunding,
			Venue:  VenueOKX,
			Symbol: sym + "USDT",
			Value:  annualized,
			Unit:   fact.UnitPctAnnualized,
			Ts:     time.Now(),
			Src:    "okx_v5",
		})
	}
	return out, nil
}

func getJSON(ctx context.Context, client *http.Client, url string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// syms D-369 S2：binance 采集器符号集（env ∪ provider 动态集）。
func (b *BinanceFunding) syms() []string { return b.cfg.effectiveSymbols() }

// syms D-369 S2：binance ticker 符号集。
func (b *BinanceTicker) syms() []string { return b.cfg.effectiveSymbols() }

// syms D-369 S2：gate/okx funding 符号集（env ∪ provider 动态集）。
func (g *GateFunding) syms() []string { return g.cfg.effectiveSymbols() }

// syms D-369 S2：okx funding 符号集。
func (o *OKXFunding) syms() []string { return o.cfg.effectiveSymbols() }
