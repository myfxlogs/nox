// dci.go —— D-183：三所 DCI/双币产品报价采集（只读）。
// 端点侦测结论（D-183 ⓪，SG 实拍）：binance sapi dci/product/list 签名可达；
// okx /api/v5/finance/sfp/dcd/products 签名可达（401 须凭据）；
// gate /api/v4/earn/dual/investment_plan **public 200** 无凭据可采。
// 覆盖 BTC+ETH；采集失败按所隔离不拖垮整轮（对齐 earn.Collect 先例）。
package cexscan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// dciUnderlyings 起步覆盖面（托管 carry 线 BTC+ETH，票面钦定）。
var dciUnderlyings = []string{"BTC", "ETH"}

// CollectDCI 合并三所 DCI 报价，单所失败 log warn 继续（不拖垮整轮）。
func (c *EarnCollector) CollectDCI(ctx context.Context) []pgstore.CexDCIQuote {
	var out []pgstore.CexDCIQuote
	if rows, err := c.collectBinanceDCI(ctx); err != nil {
		log.Printf("[cexscan] binance dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	if rows, err := c.collectOKXDCI(ctx); err != nil {
		log.Printf("[cexscan] okx dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	if rows, err := c.collectGateDCI(ctx); err != nil {
		log.Printf("[cexscan] gate dci collect: %v", err)
	} else {
		out = append(out, rows...)
	}
	return out
}

// tenorOf 交割时点→剩余天数（≥1 天取整，0/负=当日记 0）。
func tenorOf(delivery time.Time, now time.Time) int {
	d := int(math.Round(delivery.Sub(now).Hours() / 24))
	if d < 0 {
		return 0
	}
	return d
}

// ---------- Binance：GET {bnSapiURL}/v1/dci/product/list（签名） ----------

// collectBinanceDCIWithBase 可注入 base 的实现（测试用）。
// 实拍实证参数面（-1102 两轮报错）：exercisedCoin + investCoin|optionType 组——
// 按 underlying×invest(自生+USDT) 逐发取全产品矩阵（BTC/ETH 各两调）。
func (c *EarnCollector) collectBinanceDCIWithBase(ctx context.Context, base string) ([]pgstore.CexDCIQuote, error) {
	if c.bnKey == "" {
		return nil, fmt.Errorf("binance key not configured")
	}
	var out []pgstore.CexDCIQuote
	for _, u := range dciUnderlyings {
		for _, inv := range []string{u, "USDT"} {
			ms := time.Now().UnixMilli()
			query := fmt.Sprintf("exercisedCoin=%s&investCoin=%s&timestamp=%d&recvWindow=5000", u, inv, ms)
			url := fmt.Sprintf("%s/v1/dci/product/list?%s&signature=%s", base, query, binanceSign(c.bnSecret, query))
			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("X-MBX-APIKEY", c.bnKey)
			resp, err := c.client.Do(req)
			if err != nil {
				return nil, fmt.Errorf("binance dci %s/%s request: %w", u, inv, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				return nil, fmt.Errorf("binance dci %s/%s HTTP %d: %s", u, inv, resp.StatusCode, string(body))
			}
			rows, err := parseBinanceDCI(body, time.Now())
			if err != nil {
				return nil, fmt.Errorf("binance dci %s/%s: %w", u, inv, err)
			}
			out = append(out, rows...)
		}
	}
	return out, nil
}

func (c *EarnCollector) collectBinanceDCI(ctx context.Context) ([]pgstore.CexDCIQuote, error) {
	return c.collectBinanceDCIWithBase(ctx, c.bnSapiURL)
}

// parseBinanceDCI sapi dci/product/list 响应→行（exerciseCoin=underlying；apy 小数原样）。
func parseBinanceDCI(body []byte, now time.Time) ([]pgstore.CexDCIQuote, error) {
	var raw struct {
		Data []struct {
			ID             string      `json:"id"`
			ExerciseCoin   string      `json:"exerciseCoin"`
			InvestCoin     string      `json:"investCoin"`
			StrikePrice    json.Number `json:"strikePrice"`
			DeliveryDate   json.Number `json:"deliveryDate"`
			APY            json.Number `json:"apy"`
			OptionType     string      `json:"optionType"`
			CanPurchase    bool        `json:"canPurchase"`
			InstrumentName string      `json:"instrumentName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance dci decode: %w", err)
	}
	want := map[string]bool{"BTC": true, "ETH": true}
	var out []pgstore.CexDCIQuote
	for _, d := range raw.Data {
		if !want[d.ExerciseCoin] {
			continue
		}
		apy, err := d.APY.Float64()
		if err != nil {
			log.Printf("[cexscan] binance dci apy parse %q: %v", d.APY.String(), err)
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "binance", Underlying: d.ExerciseCoin,
			InvestCcy: d.InvestCoin, OptType: strings.ToUpper(d.OptionType), APY: apy,
			ProductRef: firstNonEmpty(d.ID, d.InstrumentName)}
		if v, err := d.StrikePrice.Float64(); err == nil {
			row.Strike = &v
		}
		if ms, err := d.DeliveryDate.Int64(); err == nil {
			ts := time.UnixMilli(ms)
			td := tenorOf(ts, now)
			row.DeliveryTs, row.TenorDays = &ts, &td
		}
		out = append(out, row)
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------- OKX：GET /api/v5/finance/sfp/dcd/products（签名，baseCcy×optType 组合） ----------

// okxSignPath DCI 签名头：prehash=ts+GET+path?query（okxEarnPath 同法泛化）。
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
	for _, base := range dciUnderlyings {
		for _, opt := range []string{"C", "P"} {
			path := fmt.Sprintf("/api/v5/finance/sfp/dcd/products?baseCcy=%s&quoteCcy=USDT&optType=%s", base, opt)
			body, err := okxFetch(ctx, c.client, c.okxBase, path, c.okxSignPath(path))
			if err != nil {
				return nil, fmt.Errorf("okx dci %s/%s: %w", base, opt, err)
			}
			rows, err := parseOKXDCI(body, base, opt, time.Now())
			if err != nil {
				return nil, err
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
	var out []pgstore.CexDCIQuote
	for _, d := range rawData {
		apyStr := pick(d, "apy", "estApy", "rate")
		apy, err := strconv.ParseFloat(apyStr, 64)
		if err != nil {
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "okx", Underlying: baseCcy, OptType: optType,
			InvestCcy: firstNonEmpty(pick(d, "investCcy", "quoteCcy"), "USDT"), APY: apy,
			ProductRef: pick(d, "id", "instId", "productId")}
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

func (c *EarnCollector) collectGateDCI(ctx context.Context) ([]pgstore.CexDCIQuote, error) {
	return c.collectGateDCIWithBase(ctx, gateDualURL)
}

func (c *EarnCollector) collectGateDCIWithBase(ctx context.Context, url string) ([]pgstore.CexDCIQuote, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate dci request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gate dci HTTP %d: %s", resp.StatusCode, string(body))
	}
	return parseGateDCI(body, time.Now())
}

// parseGateDCI dual investment_plan 公开数组→行（BTC/ETH exercise_currency 过滤；
// apy_display 小数年化原样；type call/put→CALL/PUT）。
func parseGateDCI(body []byte, now time.Time) ([]pgstore.CexDCIQuote, error) {
	var raw []struct {
		ID               int64       `json:"id"`
		InstrumentName   string      `json:"instrument_name"`
		Type             string      `json:"type"`
		InvestCurrency   string      `json:"invest_currency"`
		ExerciseCurrency string      `json:"exercise_currency"`
		ExercisePrice    json.Number `json:"exercise_price"`
		DeliveryTime     json.Number `json:"delivery_time"`
		Status           string      `json:"status"`
		ApyDisplay       json.Number `json:"apy_display"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("gate dci decode: %w", err)
	}
	want := map[string]bool{"BTC": true, "ETH": true}
	var out []pgstore.CexDCIQuote
	for _, d := range raw {
		if !want[d.ExerciseCurrency] {
			continue
		}
		apy, err := d.ApyDisplay.Float64()
		if err != nil {
			log.Printf("[cexscan] gate dci apy parse %q: %v", d.ApyDisplay.String(), err)
			continue
		}
		row := pgstore.CexDCIQuote{Venue: "gate", Underlying: d.ExerciseCurrency,
			InvestCcy: d.InvestCurrency, OptType: strings.ToUpper(d.Type), APY: apy,
			ProductRef: firstNonEmpty(strconv.FormatInt(d.ID, 10), d.InstrumentName)}
		if v, err := d.ExercisePrice.Float64(); err == nil {
			row.Strike = &v
		}
		if sec, err := d.DeliveryTime.Int64(); err == nil && sec > 0 {
			ts := time.Unix(sec, 0)
			td := tenorOf(ts, now)
			row.DeliveryTs, row.TenorDays = &ts, &td
		}
		out = append(out, row)
	}
	return out, nil
}
