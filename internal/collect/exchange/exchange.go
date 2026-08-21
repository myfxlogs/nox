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

// fundingPeriodsPerDay 根据 nextFundingTime 与上次结算时刻差值计算每日结算次数（D-077 修复⑪B N1）。
// 4h=6, 1h=24, 8h=3。取不到 nextFundingTime → 回退 3 并计数日志（禁静默）。
// nextFundingMs = 0 表示未取到 → 回退。
func fundingPeriodsPerDay(nextFundingMs float64) int {
	if nextFundingMs <= 0 {
		return 3 // 回退 8h×3（禁静默，调用方计数日志）
	}
	// nextFundingTime 是下一次结算时刻，与当前时间差值 ≈ 结算周期
	nowMs := float64(time.Now().UnixMilli())
	periodMs := nextFundingMs - nowMs
	if periodMs <= 0 {
		return 3 // 已过期或异常 → 回退
	}
	periodsPerDay := 24 * 3600 * 1000 / periodMs
	if periodsPerDay < 1 {
		periodsPerDay = 3
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
	Symbols        []string // 基础币种 BTC/ETH/TRX
	BinanceBaseURL string   // Binance fapi base，可指向 CF Worker 代理
	GateBaseURL    string
	OKXBaseURL     string
	Client         *http.Client
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
type BinanceFunding struct{ cfg Config }

func NewBinanceFunding(cfg Config) *BinanceFunding { return &BinanceFunding{cfg: cfg} }

func (b *BinanceFunding) Kind() string { return fact.KindFunding }

func (b *BinanceFunding) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range b.cfg.Symbols {
		pair := sym + "USDT"
		url := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", b.cfg.BinanceBaseURL, pair)
		body, err := getJSON(ctx, b.cfg.Client, url)
		if err != nil {
			return nil, fmt.Errorf("binance funding %s: %w", pair, err)
		}
		rate, ok := body["lastFundingRate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-077 修复⑪B N1：读 nextFundingTime 计算实际结算频率（4h=6, 8h=3）
		var nextFundingMs float64
		if nft, ok := body["nextFundingTime"].(float64); ok {
			nextFundingMs = nft
		}
		periodsPerDay := fundingPeriodsPerDay(nextFundingMs)
		if nextFundingMs == 0 {
			fmt.Printf("[exchange:binance] %s nextFundingTime missing, fallback 3/day\n", pair)
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
	for _, sym := range b.cfg.Symbols {
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
	for _, sym := range g.cfg.Symbols {
		contract := sym + "_USDT"
		url := fmt.Sprintf("%s/api/v4/futures/usdt/contracts/%s", g.cfg.GateBaseURL, contract)
		body, err := getJSON(ctx, g.cfg.Client, url)
		if err != nil {
			return nil, fmt.Errorf("gate funding %s: %w", contract, err)
		}
		rate, ok := body["funding_rate"].(string)
		if !ok {
			continue
		}
		var f float64
		fmt.Sscanf(rate, "%f", &f)
		// D-077 修复⑪B N1：读 next_settle_time 计算实际结算频率（Gate 返回秒级时间戳）
		var nextFundingMs float64
		if nst, ok := body["next_settle_time"].(float64); ok {
			nextFundingMs = nst * 1000 // 秒 → 毫秒
		}
		periodsPerDay := fundingPeriodsPerDay(nextFundingMs)
		if nextFundingMs == 0 {
			fmt.Printf("[exchange:gate] %s next_settle_time missing, fallback 3/day\n", sym+"USDT")
		}
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
	for _, sym := range o.cfg.Symbols {
		instID := sym + "-USDT-SWAP"
		url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", o.cfg.OKXBaseURL, instID)
		body, err := getJSON(ctx, o.cfg.Client, url)
		if err != nil {
			return nil, fmt.Errorf("okx funding %s: %w", instID, err)
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
		// D-077 修复⑪B N1：读 fundingTime 计算实际结算频率（OKX 返回毫秒级时间戳）
		var nextFundingMs float64
		if ft, ok := item["fundingTime"].(string); ok {
			fmt.Sscanf(ft, "%f", &nextFundingMs)
		}
		periodsPerDay := fundingPeriodsPerDay(nextFundingMs)
		if nextFundingMs == 0 {
			fmt.Printf("[exchange:okx] %s fundingTime missing, fallback 3/day\n", sym+"USDT")
		}
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
