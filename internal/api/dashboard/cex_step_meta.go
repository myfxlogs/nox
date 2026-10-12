package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"arb/internal/platform/httpx"
)

// D-127 P2：binance:perp stepSize 权威化。
// 运行时直连 exchangeInfo 解析 LOT_SIZE.stepSize → 内存 map；硬编码表作 fallback；
// 未知品种返回 0（fail-closed，由 P3 预检拒单）。

// cexStepMeta 缓存 binance:perp 各 symbol 的 LOT_SIZE stepSize（运行时拉取；D-466 撤硬编码 fallback）。
// 仅 binance:perp 走此表（okx perp 已由 D-125 instruments lotSz 权威化，不动）。
type cexStepMeta struct {
	mu       sync.RWMutex
	rt       map[string]float64 // 运行时表：symbol（如 BTCUSDT）→ stepSize
	infoURL  string             // exchangeInfo 直连 URL
	ttl      time.Duration      // 拉取间隔（复用 SymbolsTTL，默认 5m）
	lastPull time.Time
}

// NewCexStepMeta 构造 step 元数据缓存。infoURL 为 binance futures exchangeInfo URL。
func NewCexStepMeta(infoURL string, ttl time.Duration) *cexStepMeta {
	return &cexStepMeta{infoURL: infoURL, ttl: ttl}
}

// step 返回 symbol 的 stepSize。D-466：只查运行时表（venue 原生 exchangeInfo），
// 硬编码 fallback 表已撤（业主令「不得硬编码」；未知→0 fail-closed 由预检拒单）。
// symbol 可以是 binance 格式（BTCUSDT）或带后缀的 venue 格式（BTC-USDT-SWAP）——统一提取 base 后查。
func (m *cexStepMeta) step(symbol string) float64 {
	s, _ := m.rtLookup(symbol)
	return s
}

// rtLookup 查运行时表（RLock），尝试原始 symbol 和 base 两种 key。
func (m *cexStepMeta) rtLookup(symbol string) (float64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.rt[symbol]; ok {
		return s, true
	}
	if base := cexBaseSymbol(symbol); base != symbol {
		// 运行时表 key 可能是 BTCUSDT 格式，symbol 传入可能是 BTC-USDT-SWAP → 转 BTCUSDT 查
		normalized := cexNormalizeToBinance(symbol)
		if s, ok := m.rt[normalized]; ok {
			return s, true
		}
	}
	return 0, false
}

// refresh 直连 exchangeInfo 拉取并更新运行时表。超时 5s，失败告警 info（非 critical，有 fallback）。
// 成功后 log `[step-meta] binance loaded N symbols`。
func (m *cexStepMeta) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.infoURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpx.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errStepMetaHTTP{code: resp.StatusCode}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	parsed, err := parseBinanceExchangeInfo(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.rt = parsed
	m.lastPull = time.Now()
	m.mu.Unlock()
	log.Printf("[step-meta] binance loaded %d symbols", len(parsed))
	return nil
}

// MaybeRefresh 若距上次拉取超过 ttl 则触发一次刷新（非阻塞，失败仅 log）。
func (m *cexStepMeta) MaybeRefresh(ctx context.Context) {
	m.mu.RLock()
	need := time.Since(m.lastPull) >= m.ttl
	m.mu.RUnlock()
	if !need {
		return
	}
	if err := m.refresh(ctx); err != nil {
		log.Printf("[step-meta] binance refresh failed (fallback active): %v", err)
	}
}

// errStepMetaHTTP exchangeInfo 非 200 响应。
type errStepMetaHTTP struct{ code int }

func (e errStepMetaHTTP) Error() string { return errStepMetaHTTPMsg + strconv.Itoa(e.code) }

const errStepMetaHTTPMsg = "step-meta: exchangeInfo HTTP "

// binanceExchangeInfo exchangeInfo 响应子集（仅取 symbols[].filters LOT_SIZE）。
type binanceExchangeInfo struct {
	Symbols []struct {
		Symbol  string `json:"symbol"`
		Filters []struct {
			FilterType string `json:"filterType"`
			StepSize   string `json:"stepSize"`
		} `json:"filters"`
	} `json:"symbols"`
}

// parseBinanceExchangeInfo 解析 exchangeInfo JSON → map[symbol]stepSize。
func parseBinanceExchangeInfo(body []byte) (map[string]float64, error) {
	var info binanceExchangeInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(info.Symbols))
	for _, s := range info.Symbols {
		for _, f := range s.Filters {
			if f.FilterType != "LOT_SIZE" {
				continue
			}
			step, ok := parseFloatStr(f.StepSize)
			if !ok || step <= 0 {
				continue
			}
			out[s.Symbol] = step
		}
	}
	return out, nil
}

// cexBaseSymbol 提取基础币种（去 USDT/USD 后缀 + OKX -SWAP 后缀）。
func cexBaseSymbol(symbol string) string {
	base := symbol
	for _, suf := range []string{"-SWAP", "-USDT", "-USD", "USDT", "USD"} {
		base = strings.TrimSuffix(base, suf)
	}
	return base
}

// cexNormalizeToBinance 将 venue 格式符号转为 binance 格式（BTC-USDT-SWAP → BTCUSDT）。
// OKX perp 带 -SWAP 后缀需先去；再去连字符。
func cexNormalizeToBinance(symbol string) string {
	s := strings.TrimSuffix(symbol, "-SWAP")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

// parseFloatStr 解析浮点字符串（strconv.ParseFloat 简化封装）。
func parseFloatStr(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
