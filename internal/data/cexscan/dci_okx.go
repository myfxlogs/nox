// dci_okx.go —— D-183：OKX DCI 报价采集+解析（自 dci.go 拆分，纯移动零语义改）。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"arb/internal/store/pgstore"
)

func (c *EarnCollector) okxSignPath(path string) func(*http.Request) {
	return func(req *http.Request) {
		ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		req.Header.Set("OK-ACCESS-KEY", c.okxKey)
		req.Header.Set("OK-ACCESS-SIGN", okxSign(c.okxSecret, ts+"GET"+path))
		req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
		req.Header.Set("OK-ACCESS-PASSPHRASE", c.okxPass)
	}
}

func (c *EarnCollector) collectOKXDCI(ctx context.Context) ([]pgstore.CexDCIQuote, error) {
	if c.okxKey == "" {
		return nil, fmt.Errorf("okx key not configured")
	}
	var out []pgstore.CexDCIQuote
	for i, base := range dciUnderlyings {
		for j, opt := range []string{"C", "P"} {
			if i+j > 0 {
				time.Sleep(1200 * time.Millisecond) // 实拍实证：连发触发 429（300ms 不够，3-4调死）
			}
			path := fmt.Sprintf("/api/v5/finance/sfp/dcd/products?baseCcy=%s&quoteCcy=USDT&optType=%s", base, opt)
			body, err := okxFetch(ctx, c.client, c.okxBase, path, c.okxSignPath(path))
			if err != nil {
				log.Printf("[cexscan] okx dci %s/%s: %v（单组合隔离续）", base, opt, err)
				continue
			}
			rows, err := parseOKXDCI(body, base, opt, time.Now())
			if err != nil {
				log.Printf("[cexscan] okx dci %s/%s parse: %v（单组合隔离续）", base, opt, err)
				continue
			}
			out = append(out, rows...)
		}
	}
	return out, nil
}

// parseOKXDCI sfp/dcd/products 响应→行（map 解析——字段命名以实拍为准，
// strike/apy/expTime 键按常见形取，无键行跳过不猜）。
func parseOKXDCI(body []byte, baseCcy, optType string, now time.Time) ([]pgstore.CexDCIQuote, error) {
	var raw struct {
		Code json.Number     `json:"code"` // 实拍实证：该端点 code 是数字非字符串
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"` // 实拍实证：data 非数组（对象嵌套），两形解构
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("okx dci decode: %w", err)
	}
	if raw.Code.String() != "0" {
		return nil, fmt.Errorf("okx dci code %s: %s", raw.Code.String(), raw.Msg)
	}
	var rawData []map[string]json.RawMessage
	if err := json.Unmarshal(raw.Data, &rawData); err != nil {
		// data 对象形：常见嵌套键逐一试，命中键名+首行键集 log 留证据（字段实拍校）。
		var obj map[string]json.RawMessage
		if err2 := json.Unmarshal(raw.Data, &obj); err2 != nil {
			return nil, fmt.Errorf("okx dci data shape: %w", err)
		}
		for _, k := range []string{"products", "items", "list", "data", "records"} {
			if rm, ok := obj[k]; ok && json.Unmarshal(rm, &rawData) == nil {
				log.Printf("[cexscan] okx dci data 对象形键=%s rows=%d", k, len(rawData))
				break
			}
		}
	}
	if len(rawData) > 0 {
		keys := make([]string, 0, len(rawData[0]))
		for k := range rawData[0] {
			keys = append(keys, k)
		}
		log.Printf("[cexscan] okx dci row keys: %v", keys)
	}
	pick := func(m map[string]json.RawMessage, keys ...string) string {
		for _, k := range keys {
			rm, ok := m[k]
			if !ok {
				continue
			}
			var s string
			if err := json.Unmarshal(rm, &s); err == nil && s != "" {
				return s
			}
			var n json.Number
			if err := json.Unmarshal(rm, &n); err == nil && n.String() != "" {
				return n.String()
			}
		}
		return ""
	}
	optFull := map[string]string{"C": "CALL", "P": "PUT"}[optType]
	var out []pgstore.CexDCIQuote
	for _, d := range rawData {
		apyStr := pick(d, "annualizedYield", "apy", "estApy", "rate", "absYield") // 实拍实证：真名 annualizedYield
		apy, err := strconv.ParseFloat(apyStr, 64)
		if err != nil {
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "okx", Underlying: baseCcy, OptType: optFull,
			InvestCcy: firstNonEmpty(pick(d, "investCcy", "quoteCcy", "notionalCcy"), "USDT"), APY: apy,
			ProductRef: pick(d, "productId", "id", "instId")}
		if v, err := strconv.ParseFloat(pick(d, "strike", "strikePx"), 64); err == nil {
			row.Strike = &v
		}
		if ms, err := strconv.ParseInt(pick(d, "expTime", "deliveryTime", "maturityTime"), 10, 64); err == nil {
			ts := time.UnixMilli(ms)
			td := tenorOf(ts, now)
			row.DeliveryTs, row.TenorDays = &ts, &td
		} else if term, err := strconv.ParseFloat(pick(d, "term"), 64); err == nil {
			td := int(math.Round(term))
			row.TenorDays = &td
		}
		out = append(out, row)
	}
	return out, nil
}

// ---------- Gate：GET https://api.gateio.ws/api/v4/earn/dual/investment_plan（public） ----------

var gateDualURL = "https://api.gateio.ws/api/v4/earn/dual/investment_plan" // var=测试可覆
