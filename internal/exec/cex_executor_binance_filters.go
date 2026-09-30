// cex_executor_binance_filters.go —— D-564 S7 U10：binance 下单前本地闸
// （LOT_SIZE stepSize floor + minQty / MIN_NOTIONAL.notional / symbolConfig maxNotionalValue）。
// 脏了靠 venue 拒（-1111/-4164）改为本地 fail-closed+floor。exchangeInfo=公开端点
// （baseURL 已含 /fapi 段）；symbolConfig=签名 GET（X-MBX-APIKEY+HMAC query）。
// 规格慢变元数据允许缓存（非决策时变数据）；v3/account|positionRisk 不迁（v2 无缺陷——
// v3 flat 实测返 []是纯持仓视图不可作杠杆读，记录防后人误迁）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// binanceFilters 单 symbol 下单闸面（exchangeInfo filters 子集+symbolConfig 上限）。
type binanceFilters struct {
	StepSize    float64 // LOT_SIZE.stepSize（0=未采集→floor 不可用 fail-closed）
	MinQty      float64 // LOT_SIZE.minQty
	MinNotional float64 // MIN_NOTIONAL.notional
	MaxNotional float64 // symbolConfig.maxNotionalValue（0=未采集不拦）
	LotMaxQty   float64 // LOT_SIZE.maxQty（D-569 包B S3 量上限）
	MktMaxQty   float64 // MARKET_LOT_SIZE.maxQty（同上；stepSize 实拍=0 不采）
	at          time.Time
}

// binanceFiltersTTL 过滤器缓存有效期（D-569 包B S6：24h 超龄视 miss 重取）。
const binanceFiltersTTL = 24 * time.Hour

var (
	binanceFiltersMu    sync.Mutex
	binanceFiltersCache = map[string]binanceFilters{}
)

func binanceFiltersLoad(symbol string) (binanceFilters, bool) {
	binanceFiltersMu.Lock()
	defer binanceFiltersMu.Unlock()
	f, ok := binanceFiltersCache[symbol]
	if !ok || time.Since(f.at) > binanceFiltersTTL {
		return binanceFilters{}, false
	}
	return f, true
}

// binanceFiltersInvalidate D-569 包B S6：交易所拒单指过滤器漂移 → perp+spot 两表同删
// （同 symbol 两域缓存，下一单各自重取）。
func binanceFiltersInvalidate(symbol string) {
	binanceFiltersMu.Lock()
	delete(binanceFiltersCache, symbol)
	binanceFiltersMu.Unlock()
	spotFiltersMu.Lock()
	delete(spotFiltersCache, symbol)
	spotFiltersMu.Unlock()
}

func binanceFiltersStore(symbol string, f binanceFilters) {
	binanceFiltersMu.Lock()
	binanceFiltersCache[symbol] = f
	binanceFiltersMu.Unlock()
}

// binanceFiltersFetch exchangeInfo（公开）解析单 symbol filters。
func (e *CEXExecutor) binanceFiltersFetch(ctx context.Context, symbol string) (binanceFilters, error) {
	body, err := e.get(ctx, e.baseURL+"/v1/exchangeInfo", nil)
	if err != nil {
		return binanceFilters{}, fmt.Errorf("exchangeInfo: %w", err)
	}
	var out struct {
		Symbols []struct {
			Symbol  string `json:"symbol"`
			Filters []struct {
				FilterType string `json:"filterType"`
				StepSize   string `json:"stepSize"`
				MinQty     string `json:"minQty"`
				MaxQty     string `json:"maxQty"`
				Notional   string `json:"notional"`
			} `json:"filters"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return binanceFilters{}, fmt.Errorf("exchangeInfo json: %w", err)
	}
	for _, s := range out.Symbols {
		if s.Symbol != symbol {
			continue
		}
		f := binanceFilters{at: time.Now()}
		for _, fl := range s.Filters {
			switch fl.FilterType {
			case "LOT_SIZE":
				f.StepSize, _ = strconv.ParseFloat(fl.StepSize, 64)
				f.MinQty, _ = strconv.ParseFloat(fl.MinQty, 64)
				f.LotMaxQty, _ = strconv.ParseFloat(fl.MaxQty, 64)
			case "MARKET_LOT_SIZE":
				f.MktMaxQty, _ = strconv.ParseFloat(fl.MaxQty, 64)
			case "MIN_NOTIONAL":
				f.MinNotional, _ = strconv.ParseFloat(fl.Notional, 64)
			}
		}
		if f.StepSize <= 0 {
			return binanceFilters{}, fmt.Errorf("exchangeInfo %s: LOT_SIZE.stepSize 缺/非法", symbol)
		}
		binanceFiltersStore(symbol, f)
		return f, nil
	}
	return binanceFilters{}, fmt.Errorf("exchangeInfo %s: symbol 不在表", symbol)
}

// binanceMaxNotional symbolConfig（签名 GET /v1/symbolConfig?symbol=X）→ maxNotionalValue。
// D-569 S4 R1 F-1：归一 signedBinanceRequest（wire 形状逐字等价：GET+query+末位 signature+
// X-MBX-APIKEY 由 Raw 统一构造）——自动获 httpStatusError 类型化+(-1021) delta 校时伞盖。
func (e *CEXExecutor) binanceMaxNotional(ctx context.Context, symbol string) (float64, error) {
	rows, err := e.signedBinanceRequestList(ctx, "GET", "/v1/symbolConfig", url.Values{"symbol": {symbol}}) // D-587：官方返回数组
	if err != nil {
		return 0, fmt.Errorf("symbolConfig %s: %w", symbol, err)
	}
	if len(rows) == 0 {
		return 0, nil // 空表=未采不拦（语义不变）
	}
	f, _ := strconv.ParseFloat(asString(rows[0]["maxNotionalValue"]), 64)
	return f, nil
}

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

// binanceQtyGuard D-564 S7 U10：下单前本地闸。
// floor=LOT_SIZE.stepSize 向下取整（floor 后 <minQty → 拒「不足最小量」）；
// mark>0 时 MIN_NOTIONAL/maxNotional 判（qty×mark）。返回生效 qty。
// 仅 perp 开仓全闸；平仓调用方可传 mark=0（只 floor，名义闸不适用于 reduce）。
func (e *CEXExecutor) binanceQtyGuard(ctx context.Context, symbol string, qty, mark float64) (float64, error) {
	f, ok := binanceFiltersLoad(symbol)
	if !ok {
		var err error
		if f, err = e.binanceFiltersFetch(ctx, symbol); err != nil {
			return 0, err
		}
	}
	q := qty
	if f.StepSize > 0 {
		q = math.Floor(qty/f.StepSize) * f.StepSize
	} else {
		return 0, fmt.Errorf("binance %s stepSize 未采集（fail-closed 不发裸量单）", symbol)
	}
	if f.MinQty > 0 && q < f.MinQty {
		return 0, fmt.Errorf("binance %s 不足最小量：%.6f < minQty %.6f fail-closed", symbol, q, f.MinQty)
	}
	// D-569 包B S3：量上限双卡（MARKET_LOT_SIZE/LOT_SIZE 任一超即拒；平仓 mark=0 同卡）。
	if f.MktMaxQty > 0 && q > f.MktMaxQty {
		return 0, fmt.Errorf("binance %s 超市量上限：%.6f > MARKET_LOT_SIZE %.6f fail-closed", symbol, q, f.MktMaxQty)
	}
	if f.LotMaxQty > 0 && q > f.LotMaxQty {
		return 0, fmt.Errorf("binance %s 超限量上限：%.6f > LOT_SIZE %.6f fail-closed", symbol, q, f.LotMaxQty)
	}
	if mark > 0 {
		if f.MaxNotional == 0 { // symbolConfig 未采（exchangeInfo 无此字段）——补采一次并入库
			if mn, err := e.binanceMaxNotional(ctx, symbol); err == nil && mn > 0 {
				f.MaxNotional = mn
				binanceFiltersStore(symbol, f)
			} else if err != nil { // 采败=上限面不拦（warn 语义，不阻断主判定）——但留痕不静默
				log.Printf("[exec] binance %s symbolConfig 补采失败（maxNotional 上限面本轮不拦）: %v", symbol, err)
			}
		}
		notional := q * mark
		if f.MinNotional > 0 && notional < f.MinNotional {
			return 0, fmt.Errorf("binance %s 名义不足：%.2f < MIN_NOTIONAL %.2f fail-closed", symbol, notional, f.MinNotional)
		}
		if f.MaxNotional > 0 && notional > f.MaxNotional {
			return 0, fmt.Errorf("binance %s 超合约名义上限：%.2f > maxNotional %.2f fail-closed", symbol, notional, f.MaxNotional)
		}
	}
	return q, nil
}
