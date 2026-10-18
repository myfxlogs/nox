// tradifi_syms.go —— TRADIFI_PERPETUAL 品种枚举轴（D-624 前置闸②配套）：
// fapi/v1/exchangeInfo 周期实拉（venue 实读禁硬编——contractType 机器枚举），
// TRADIFI_PERPETUAL+TRADING 品种集喂 SymProvider → 既有 funding/markPrice
// 采集轴（premiumIndex/markPrice 帧 allowedSymbolSet 命中即产 facts）自动扩面。
// 读失败保旧集（同 watchlist provider 语义——符号集闪断不塌）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TradifiSymbols TRADIFI_PERPETUAL 品种供给器（10min 周期刷新由装配侧驱动）。
type TradifiSymbols struct {
	base   string // fapi REST base（含 /fapi 段；空=原生域默认）
	client *http.Client
	mu     sync.RWMutex
	syms   []string
}

// NewTradifiSymbols 构造（base=fapi base URL；空=venue 原生域 fapi.binance.com）。
func NewTradifiSymbols(base string) *TradifiSymbols {
	return &TradifiSymbols{base: strings.TrimRight(base, "/"), client: http.DefaultClient}
}

// SetClient 注入 http client（装配面=httpx.Client()——proxy CA/token 同径）。
func (t *TradifiSymbols) SetClient(c *http.Client) { t.client = c }

// Refresh 实拉 exchangeInfo——contractType=TRADIFI_PERPETUAL 且 status=TRADING
// 的 symbol 枚举落集（venue 实读目录，禁硬编品种表）。败=保旧集只记日志。
func (t *TradifiSymbols) Refresh(ctx context.Context) error {
	base := t.base
	if base == "" {
		base = "https://fapi.binance.com/fapi"
	}
	url := base + "/v1/exchangeInfo"
	client := t.client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("tradifi exchangeInfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("tradifi exchangeInfo HTTP %d", resp.StatusCode)
	}
	var info struct {
		Symbols []struct {
			Symbol       string `json:"symbol"`
			ContractType string `json:"contractType"`
			Status       string `json:"status"`
			QuoteAsset   string `json:"quoteAsset"`
		} `json:"symbols"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("tradifi exchangeInfo 解析: %w", err)
	}
	var syms []string
	for _, s := range info.Symbols {
		if s.ContractType == "TRADIFI_PERPETUAL" && s.Status == "TRADING" && s.Symbol != "" {
			syms = append(syms, s.Symbol)
		}
	}
	t.mu.Lock()
	t.syms = syms
	t.mu.Unlock()
	log.Printf("[tradifi] exchangeInfo 枚举 %d 品种（TRADIFI_PERPETUAL×TRADING）", len(syms))
	return nil
}

// Symbols 实现 SymbolProvider——binance 独供（其余 venue 空集）。
// 零品种/未刷新=空集如实（不臆造）。
func (t *TradifiSymbols) Symbols(venue string) []string {
	if venue != VenueBinance {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string(nil), t.syms...)
}

// MultiSymbolProvider 多源并集（watchlist ∪ tradifi——Symbols(venue) 逐源合流去重）。
type MultiSymbolProvider []SymbolProvider

// Symbols 逐源并集去重（nil 源/空集跳过；全空=空集如实）。
func (m MultiSymbolProvider) Symbols(venue string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range m {
		if p == nil {
			continue
		}
		for _, s := range p.Symbols(venue) {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// StartTradifiRefresh 周期刷新循环（首拍立即——装配侧 safego.GoLoop 挂）。
func (t *TradifiSymbols) StartTradifiRefresh(ctx context.Context) {
	_ = t.Refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
			_ = t.Refresh(ctx)
		}
	}
}
