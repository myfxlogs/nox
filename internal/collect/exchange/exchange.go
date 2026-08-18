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

// Config CEX 采集配置。
type Config struct {
	Symbols      []string // 基础币种 BTC/ETH/TRX
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
annualized := f * 3 * 365 * 100
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
Symbol: symbol, Venue: venue, VenueType: venueType,
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
		// 年化 = funding_rate * 3 * 365 / 100（8h 结算 = 3次/天，转百分比）
		annualized := f * 3 * 365 * 100
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
		// 年化 = funding_rate * 3 * 365 / 100（8h 结算）
		annualized := f * 3 * 365 * 100
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

// GateTicker 采集 Gate.io best bid/ask（REST 轮询，§3.3）。
type GateTicker struct {
	cfg Config
}

func NewGateTicker(cfg Config) *GateTicker { return &GateTicker{cfg: cfg} }

// PollQuotes 采集报价（spot + perp）。
func (g *GateTicker) PollQuotes(ctx context.Context) ([]bus.Quote, error) {
	var out []bus.Quote
	for _, sym := range g.cfg.Symbols {
		pair := sym + "_USDT"
		// spot
		spotURL := fmt.Sprintf("%s/api/v4/spot/tickers?currency_pair=%s", g.cfg.GateBaseURL, pair)
		if q, err := fetchGateSpotTicker(ctx, g.cfg.Client, spotURL, sym+"USDT", VenueGate, "spot"); err == nil {
			out = append(out, q)
		}
		// perp
		perpURL := fmt.Sprintf("%s/api/v4/futures/usdt/tickers?contract=%s", g.cfg.GateBaseURL, pair)
		if q, err := fetchGatePerpTicker(ctx, g.cfg.Client, perpURL, sym+"USDT", VenueGate, "perp"); err == nil {
			out = append(out, q)
		}
	}
	return out, nil
}

func fetchGateSpotTicker(ctx context.Context, client *http.Client, url, symbol, venue, venueType string) (bus.Quote, error) {
	body, err := getJSONArray(ctx, client, url)
	if err != nil || len(body) == 0 {
		return bus.Quote{}, fmt.Errorf("gate spot: %w", err)
	}
	item, ok := body[0].(map[string]any)
	if !ok {
		return bus.Quote{}, fmt.Errorf("gate spot: bad response")
	}
	bid, _ := item["highest_bid"].(string)
	ask, _ := item["lowest_ask"].(string)
	var bidF, askF float64
	fmt.Sscanf(bid, "%f", &bidF)
	fmt.Sscanf(ask, "%f", &askF)
	return bus.Quote{
		Symbol:    symbol,
		Venue:     venue,
		VenueType: venueType,
		Bid:       bidF,
		Ask:       askF,
		Time:      time.Now(),
	}, nil
}

func fetchGatePerpTicker(ctx context.Context, client *http.Client, url, symbol, venue, venueType string) (bus.Quote, error) {
	body, err := getJSONArray(ctx, client, url)
	if err != nil || len(body) == 0 {
		return bus.Quote{}, fmt.Errorf("gate perp: %w", err)
	}
	item, ok := body[0].(map[string]any)
	if !ok {
		return bus.Quote{}, fmt.Errorf("gate perp: bad response")
	}
	last, _ := item["last"].(string)
	var mid float64
	fmt.Sscanf(last, "%f", &mid)
	// Gate perp ticker 只有 last price，用 mark_price 做 bid/ask 近似
	mark, _ := item["mark_price"].(string)
	var markF float64
	fmt.Sscanf(mark, "%f", &markF)
	if markF == 0 {
		markF = mid
	}
	return bus.Quote{
		Symbol:    symbol,
		Venue:     venue,
		VenueType: venueType,
		Bid:       markF,
		Ask:       markF,
		Time:      time.Now(),
	}, nil
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

func getJSONArray(ctx context.Context, client *http.Client, url string) ([]any, error) {
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
	var arr []any
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}
