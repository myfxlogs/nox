// cex_executor_binance_filters.go —— D-564 S7 U10：binance 下单前本地闸
// （LOT_SIZE stepSize floor + minQty / MIN_NOTIONAL.notional / symbolConfig maxNotionalValue）。
// 脏了靠 venue 拒（-1111/-4164）改为本地 fail-closed+floor。exchangeInfo=公开端点
// （baseURL 已含 /fapi 段）；symbolConfig=签名 GET（X-MBX-APIKEY+HMAC query）。
// 规格慢变元数据允许缓存（非决策时变数据）；v3/account|positionRisk 不迁（v2 无缺陷——
// v3 flat 实测返 []是纯持仓视图不可作杠杆读，记录防后人误迁）。
package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
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
}

var (
	binanceFiltersMu    sync.Mutex
	binanceFiltersCache = map[string]binanceFilters{}
)

func binanceFiltersLoad(symbol string) (binanceFilters, bool) {
	binanceFiltersMu.Lock()
	defer binanceFiltersMu.Unlock()
	f, ok := binanceFiltersCache[symbol]
	return f, ok
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
		f := binanceFilters{}
		for _, fl := range s.Filters {
			switch fl.FilterType {
			case "LOT_SIZE":
				f.StepSize, _ = strconv.ParseFloat(fl.StepSize, 64)
				f.MinQty, _ = strconv.ParseFloat(fl.MinQty, 64)
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
func (e *CEXExecutor) binanceMaxNotional(ctx context.Context, symbol string) (float64, error) {
	query := "symbol=" + symbol + "&timestamp=" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "&recvWindow=5000"
	mac := hmac.New(sha256.New, []byte(e.apiSecret))
	mac.Write([]byte(query))
	sign := hex.EncodeToString(mac.Sum(nil))
	body, err := e.get(ctx, e.baseURL+"/v1/symbolConfig?"+query+"&signature="+sign, http.Header{
		"X-MBX-APIKEY": {e.apiKey},
	})
	if err != nil {
		return 0, fmt.Errorf("symbolConfig %s: %w", symbol, err)
	}
	var sc struct {
		MaxNotionalValue float64 `json:"maxNotionalValue"`
	}
	if err := json.Unmarshal(body, &sc); err != nil {
		return 0, fmt.Errorf("symbolConfig json: %w", err)
	}
	return sc.MaxNotionalValue, nil
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
