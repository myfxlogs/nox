package exec

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arb/internal/httpx"
)

// CEXExecutor 是 Binance/OKX REST 签名下单执行器（§13.2）。
// 每个市场一个实例（spot/perp 是不同实例）。D-059：补 OKX 下单（OK-ACCESS-* 四头 +
// base64 HMAC + x-simulated-trading:1 模拟盘），venue 分派。
type CEXExecutor struct {
	venue      string
	venueType  string // spot / perp
	apiKey     string
	apiSecret  string
	passphrase string // OKX 必填；Binance 空
	baseURL    string // binance: proxy/demo 根；okx: www.okx.com
	simulated  bool   // OKX 模拟盘：请求带 x-simulated-trading:1（demo key 必开，D-054 同款）
	client     *http.Client
}

// NewCEXExecutor 创建 CEX 执行器。
// baseURL 为 venue 根（binance 走代理 fapi 风格需已含 /demo 路径；okx 实盘域名）。
// simulated 仅 OKX 生效（Binance demo 由 baseURL 的 /demo 前缀路由，D-055）。
func NewCEXExecutor(venue, venueType, apiKey, apiSecret, passphrase, baseURL string, simulated bool) *CEXExecutor {
	return &CEXExecutor{
		venue:      venue,
		venueType:  venueType,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		baseURL:    baseURL,
		simulated:  simulated,
		client:     httpx.Client(), // CA 信任 + X-Proxy-Token 注入（D-055）；OKX 公网忽略注入头
	}
}

func (e *CEXExecutor) Key() string { return e.venue + ":" + e.venueType }

func (e *CEXExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	switch e.venue {
	case "binance":
		return e.placeBinance(ctx, o)
	case "okx":
		return e.placeOKX(ctx, o)
	default:
		return Fill{}, fmt.Errorf("cex executor: unsupported venue %q", e.venue)
	}
}

// placeBinance 下单：spot /api/v3/order、perp /fapi/v1/order，HMAC-SHA256 hex 签名 + X-MBX-APIKEY。
func (e *CEXExecutor) placeBinance(ctx context.Context, o Order) (Fill, error) {
	side := "BUY"
	if o.Side == "SELL" {
		side = "SELL"
	}
	params := url.Values{}
	params.Set("symbol", o.Symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", strconv.FormatFloat(o.Qty, 'f', -1, 64))

	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/fapi/v1/order"
	}

	body, err := e.signedBinanceRequest(ctx, "POST", endpoint, params)
	if err != nil {
		return Fill{}, fmt.Errorf("cex order: %w", err)
	}

	execType, _ := body["executedQty"].(string)
	orderID, _ := body["orderId"].(json.Number)
	if orderID == "" {
		if id, ok := body["orderId"].(float64); ok {
			orderID = json.Number(strconv.FormatFloat(id, 'f', 0, 64))
		}
	}

	qty, _ := strconv.ParseFloat(execType, 64)
	avgPrice := binanceOrderAvgPrice(body, qty)

	return Fill{
		OrderID: string(orderID),
		Price:   avgPrice,
		Qty:     qty,
		Ts:      time.Now(),
	}, nil
}

// binanceOrderAvgPrice 从 Binance 下单响应计算成交均价（D-059 实测：下单响应无 avgPrice 字段，
// 只有 fills 数组 + cummulativeQuoteQty；price 字段 MARKET 单恒 0。均价 = Σ(fill.price×qty)/Σqty，
// 无 fills 时回退 cummulativeQuoteQty/executedQty）。
func binanceOrderAvgPrice(body map[string]any, qty float64) float64 {
	if fills, ok := body["fills"].([]any); ok && len(fills) > 0 {
		var sumQty, sumVal float64
		for _, f := range fills {
			m, ok := f.(map[string]any)
			if !ok {
				continue
			}
			p, _ := strconv.ParseFloat(asString(m["price"]), 64)
			q, _ := strconv.ParseFloat(asString(m["qty"]), 64)
			sumVal += p * q
			sumQty += q
		}
		if sumQty > 0 {
			return sumVal / sumQty
		}
	}
	if cqqStr, ok := body["cummulativeQuoteQty"].(string); ok && qty > 0 {
		if cqq, err := strconv.ParseFloat(cqqStr, 64); err == nil {
			return cqq / qty
		}
	}
	return 0
}

// asString 容错取 string 字段（json.Number / float64 / string 都接受）。
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// placeOKX 下单：POST /api/v5/trade/order（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret,
// ts+method+path+body))）。spot tdMode=cash；perp tdMode=cross（代码保留，本次不验证）。
// 模拟盘带 x-simulated-trading:1（OKX demo key 必开，D-054 同款）。
func (e *CEXExecutor) placeOKX(ctx context.Context, o Order) (Fill, error) {
	side := "buy"
	if o.Side == "SELL" {
		side = "sell"
	}
	tdMode := "cash" // spot
	instID := o.Symbol
	if e.venueType == "perp" {
		tdMode = "cross"
		if !strings.HasSuffix(instID, "-SWAP") {
			instID = instID + "-SWAP" // OKX perp instId 形如 BTC-USDT-SWAP
		}
	}
	bodyStr := fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q}`,
		instID, tdMode, side, strconv.FormatFloat(o.Qty, 'f', -1, 64))

	path := "/api/v5/trade/order"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "POST" + path + bodyStr
	sign := okxSign(e.apiSecret, prehash)

	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}

	body, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID     string `json:"ordId"`
			SCode     string `json:"sCode"`
			SMsg      string `json:"sMsg"`
			AvgPx     string `json:"avgPx"`
			AccFillSz string `json:"accFillSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Fill{}, fmt.Errorf("okx order json: %w", err)
	}
	if resp.Code != "0" {
		return Fill{}, fmt.Errorf("okx order: code=%s msg=%s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 {
		return Fill{}, fmt.Errorf("okx order: empty data")
	}
	d := resp.Data[0]
	if d.SCode != "" && d.SCode != "0" {
		return Fill{}, fmt.Errorf("okx order: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	qty, _ := strconv.ParseFloat(d.AccFillSz, 64)
	avgPx, _ := strconv.ParseFloat(d.AvgPx, 64)
	return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, Ts: time.Now()}, nil
}

func (e *CEXExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	// CEX 平仓 = 反向市价单
	symbol := ref.Symbol
	// 推断 side：CEX 没有"close"概念，需要外部传入方向
	// 简化：CloseOrder 在 CEX 场景由执行管线直接调 PlaceOrder 反向
	// 此方法保留接口完整性，实际 CEX 平仓走 PlaceOrder
	_ = symbol
	return Fill{}, fmt.Errorf("cex: use PlaceOrder with opposite side for close")
}

// signedBinanceRequest 发 Binance 签名请求（HMAC hex + X-MBX-APIKEY）。
func (e *CEXExecutor) signedBinanceRequest(ctx context.Context, method, endpoint string, params url.Values) (map[string]any, error) {
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")

	signature := binanceSign(e.apiSecret, params.Encode())
	params.Set("signature", signature)

	u := e.baseURL + endpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return m, nil
}

// post 发带签名头的 POST JSON 请求并读回 body（非 200 → 错误带响应体）。
func (e *CEXExecutor) post(ctx context.Context, url, bodyStr string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(bodyStr))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// binanceSign HMAC-SHA256 hex 签名（与 reconcile 同法，各包独立实现——算法非事实，P3 不适用）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, ts+method+path+body))（与 reconcile 同法）。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
