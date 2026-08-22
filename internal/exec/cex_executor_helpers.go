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
)

// binanceOrderStatus 查询 Binance 订单成交（perp /fapi/v1/order；spot /api/v3/order，签名 GET）。
func (e *CEXExecutor) binanceOrderStatus(ctx context.Context, symbol, ordID string) (struct {
	Status      string `json:"status"`
	ExecutedQty string `json:"executedQty"`
	AvgPrice    string `json:"avgPrice"`
}, error) {
	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/fapi/v1/order"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", ordID)
	body, err := e.signedBinanceRequest(ctx, "GET", endpoint, params)
	if err != nil {
		return struct {
			Status      string `json:"status"`
			ExecutedQty string `json:"executedQty"`
			AvgPrice    string `json:"avgPrice"`
		}{}, err
	}
	return struct {
		Status      string `json:"status"`
		ExecutedQty string `json:"executedQty"`
		AvgPrice    string `json:"avgPrice"`
	}{Status: asString(body["status"]), ExecutedQty: asString(body["executedQty"]), AvgPrice: asString(body["avgPrice"])}, nil
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

// okxOrderStatus 查询单笔订单成交（GET /api/v5/trade/order，签名四头 + simulated 头）。
func (e *CEXExecutor) okxOrderStatus(ctx context.Context, instID, ordID string) (struct {
	AccFillSz          string `json:"accFillSz"`
	AvgPx              string `json:"avgPx"`
	State              string `json:"state"`
	CancelSourceReason string `json:"cancelSourceReason"`
}, error) {
	var out struct {
		AccFillSz          string `json:"accFillSz"`
		AvgPx              string `json:"avgPx"`
		State              string `json:"state"`
		CancelSourceReason string `json:"cancelSourceReason"`
	}
	path := "/api/v5/trade/order?instId=" + url.QueryEscape(instID) + "&ordId=" + url.QueryEscape(ordID)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return out, err
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			AccFillSz          string `json:"accFillSz"`
			AvgPx              string `json:"avgPx"`
			State              string `json:"state"`
			CancelSourceReason string `json:"cancelSourceReason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out, fmt.Errorf("okx order status json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return out, fmt.Errorf("okx order status: code=%s", resp.Code)
	}
	d := resp.Data[0]
	out.AccFillSz, out.AvgPx, out.State, out.CancelSourceReason = d.AccFillSz, d.AvgPx, d.State, d.CancelSourceReason
	return out, nil
}

// OKXPosition 是一条 OKX 持仓（GET /api/v5/account/positions，D-059 实施修正② 自愈清理用）。
type OKXPosition struct {
	InstID  string
	PosSide string // long / short / net
	Pos     float64
	AvgPx   float64
}

// OKXPositions 查询 OKX 指定 instId 的当前持仓（long_short_mode 下 long/short 分列）。
// 供平仓核对 / demo 测试自愈清理（旧 bug 残留的双向持仓要能关掉），签名四头 + simulated 头。
func (e *CEXExecutor) OKXPositions(ctx context.Context, instID string) ([]OKXPosition, error) {
	path := "/api/v5/account/positions?instId=" + url.QueryEscape(instID)
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID  string `json:"instId"`
			PosSide string `json:"posSide"`
			Pos     string `json:"pos"`
			AvgPx   string `json:"avgPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx positions json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx positions: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var out []OKXPosition
	for _, d := range resp.Data {
		if d.InstID != instID {
			continue
		}
		p, _ := strconv.ParseFloat(d.Pos, 64)
		if p == 0 {
			continue
		}
		a, _ := strconv.ParseFloat(d.AvgPx, 64)
		out = append(out, OKXPosition{InstID: d.InstID, PosSide: d.PosSide, Pos: p, AvgPx: a})
	}
	return out, nil
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

// get 发带签名头的 GET 请求并读回 body（非 200 → 错误带响应体）。
func (e *CEXExecutor) get(ctx context.Context, url string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
