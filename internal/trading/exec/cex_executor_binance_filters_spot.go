// cex_executor_binance_filters_spot.go —— D-569 包A S2：Binance spot 过滤器独立表与
// 本地闸（perp 域见 cex_executor_binance_filters.go；D-018 阶段b 拆分 ≤300 契约）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
)

// --- D-569 包A S2：spot 过滤器独立表（与 perp binanceFiltersCache 分表——同 symbol 值不同实证）---

// binanceSpotFilters 单 symbol spot 下单闸面（/v3/exchangeInfo filters 子集）。
type binanceSpotFilters struct {
	StepSize, MinQty, MinNotional, MaxNotional, LotMaxQty, MktMaxQty float64
	ApplyMin, ApplyMax                                               bool // NOTIONAL applyMin/MaxToMarket（旧形 MIN_NOTIONAL → min=true/max=false）
	at                                                               time.Time
}

var (
	spotFiltersMu    sync.Mutex
	spotFiltersCache = map[string]binanceSpotFilters{}
)

// binanceSpotFiltersFetch GET /v3/exchangeInfo?symbol=（公开端点无签名；spot honor symbol= 精准拉）。
// 过滤器缺席条目=0 值（闸只在官方过滤器存在时生效）；MARKET_LOT_SIZE 只采 maxQty（minQty/stepSize 实拍=0）。
// NOTIONAL 新形与 MIN_NOTIONAL 旧形双解（旧形→applyMin=true/applyMax=false/maxNotional=0）。
func (e *CEXExecutor) binanceSpotFiltersFetch(ctx context.Context, symbol string) (binanceSpotFilters, error) {
	body, err := e.get(ctx, e.baseURL+"/v3/exchangeInfo?symbol="+symbol, nil)
	if err != nil {
		return binanceSpotFilters{}, fmt.Errorf("exchangeInfo: %w", err)
	}
	var out struct {
		Symbols []struct {
			Symbol  string `json:"symbol"`
			Filters []struct {
				FilterType       string `json:"filterType"`
				StepSize         string `json:"stepSize"`
				MinQty           string `json:"minQty"`
				MaxQty           string `json:"maxQty"`
				MinNotional      string `json:"minNotional"`
				MaxNotional      string `json:"maxNotional"`
				Notional         string `json:"notional"`
				ApplyMinToMarket bool   `json:"applyMinToMarket"`
				ApplyMaxToMarket bool   `json:"applyMaxToMarket"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return binanceSpotFilters{}, fmt.Errorf("exchangeInfo json: %w", err)
	}
	for _, s := range out.Symbols {
		if s.Symbol != symbol {
			continue
		}
		f := binanceSpotFilters{at: time.Now()}
		for _, fl := range s.Filters {
			switch fl.FilterType {
			case "LOT_SIZE":
				f.StepSize, _ = strconv.ParseFloat(fl.StepSize, 64)
				f.MinQty, _ = strconv.ParseFloat(fl.MinQty, 64)
				f.LotMaxQty, _ = strconv.ParseFloat(fl.MaxQty, 64)
			case "MARKET_LOT_SIZE":
				f.MktMaxQty, _ = strconv.ParseFloat(fl.MaxQty, 64)
			case "NOTIONAL":
				f.MinNotional, _ = strconv.ParseFloat(fl.MinNotional, 64)
				f.MaxNotional, _ = strconv.ParseFloat(fl.MaxNotional, 64)
				f.ApplyMin = fl.ApplyMinToMarket
				f.ApplyMax = fl.ApplyMaxToMarket
			case "MIN_NOTIONAL":
				f.MinNotional, _ = strconv.ParseFloat(fl.MinNotional, 64)
				f.ApplyMin = true
				f.ApplyMax = false
				f.MaxNotional = 0
			}
		}
		spotFiltersMu.Lock()
		spotFiltersCache[symbol] = f
		spotFiltersMu.Unlock()
		return f, nil
	}
	return binanceSpotFilters{}, fmt.Errorf("exchangeInfo %s: symbol 不在表", symbol)
}

// binanceSpotQtyGuard D-569 包A S2：spot 下单前本地闸（闸序设计 §2.1② 钉死禁换）。
// 返回 floor 后生效 qty；全 fail-closed（取价败/过滤器拉败均拒单不猜）。
func (e *CEXExecutor) binanceSpotQtyGuard(ctx context.Context, symbol string, qty float64) (float64, error) {
	spotFiltersMu.Lock()
	f, ok := spotFiltersCache[symbol]
	spotFiltersMu.Unlock()
	if ok && time.Since(f.at) > binanceFiltersTTL { // D-569 包B S6：超龄视 miss 重取（与 perp 同 TTL）
		ok = false
	}
	if !ok {
		var err error
		if f, err = e.binanceSpotFiltersFetch(ctx, symbol); err != nil {
			return 0, err
		}
	}
	// 1. floor 到 stepSize——KB float-floor-qty-precision-artifact：按 step 小数位 round 回
	// 干净值再下发（防 1.4000000000000001 伪影超 venue 精度=-1111）。
	q := qty
	if f.StepSize > 0 {
		dec := int(math.Round(-math.Log10(f.StepSize)))
		scale := math.Pow10(dec)
		q = math.Round(math.Floor(qty/f.StepSize)*f.StepSize*scale+1e-9) / scale
	}
	// 2. minQty
	if f.MinQty > 0 && q < f.MinQty {
		return 0, fmt.Errorf("binance spot %s 不足最小量：%.8f < minQty %.8f fail-closed", symbol, q, f.MinQty)
	}
	// 3. 量上限（MARKET_LOT_SIZE 优先判定序在 LOT_SIZE 前——市价单语义闸）
	if f.MktMaxQty > 0 && q > f.MktMaxQty {
		return 0, fmt.Errorf("binance spot %s 超市量上限：%.8f > MARKET_LOT_SIZE %.8f fail-closed", symbol, q, f.MktMaxQty)
	}
	if f.LotMaxQty > 0 && q > f.LotMaxQty {
		return 0, fmt.Errorf("binance spot %s 超限量上限：%.8f > LOT_SIZE %.8f fail-closed", symbol, q, f.LotMaxQty)
	}
	// 4. 名义闸条件取价（R1 F-3）：两 apply 皆 false → 不取价不发 GET；取败 fail-closed 拒。
	if (f.MinNotional > 0 && f.ApplyMin) || (f.MaxNotional > 0 && f.ApplyMax) {
		last, perr := e.spotMarkPrice(ctx, symbol)
		if perr != nil {
			return 0, fmt.Errorf("binance spot %s 名义闸取价失败：%w fail-closed", symbol, perr)
		}
		notional := q * last
		if f.MinNotional > 0 && f.ApplyMin && notional < f.MinNotional {
			return 0, fmt.Errorf("binance spot %s 名义不足：%.2f < minNotional %.2f fail-closed", symbol, notional, f.MinNotional)
		}
		if f.MaxNotional > 0 && f.ApplyMax && notional > f.MaxNotional {
			return 0, fmt.Errorf("binance spot %s 超名义上限：%.2f > maxNotional %.2f fail-closed", symbol, notional, f.MaxNotional)
		}
	}
	return q, nil
}
