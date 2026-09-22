package exec

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
// D-391 S2：+fee/feeCcy——实证（2026-09-22 pos498 ordId=3943917115077283840 实拍）：
// order 响应含累计 fee="-2.18095" feeCcy="USDT"（负号=付出）。
func (e *CEXExecutor) okxOrderStatus(ctx context.Context, instID, ordID string) (struct {
	AccFillSz          string `json:"accFillSz"`
	AvgPx              string `json:"avgPx"`
	State              string `json:"state"`
	CancelSourceReason string `json:"cancelSourceReason"`
	Fee                string `json:"fee"`
	FeeCcy             string `json:"feeCcy"`
}, error) {
	var out struct {
		AccFillSz          string `json:"accFillSz"`
		AvgPx              string `json:"avgPx"`
		State              string `json:"state"`
		CancelSourceReason string `json:"cancelSourceReason"`
		Fee                string `json:"fee"`
		FeeCcy             string `json:"feeCcy"`
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
			Fee                string `json:"fee"`
			FeeCcy             string `json:"feeCcy"`
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
	out.Fee, out.FeeCcy = d.Fee, d.FeeCcy
	return out, nil
}

// okxFeeToUSD D-391 S2：OKX order/fills 响应 fee 字段 → USD 正数成本。
// venue 原始负号=付出（实证 fee="-2.18095"）→ 取绝对值；feeCcy 非 USD 稳定币 → ok=false
// （fail-open 回退 NULL，不猜换算——KB unspecified-err-handling-defaults-unsafe）。
func okxFeeToUSD(feeStr, feeCcy string) (float64, bool) {
	if feeStr == "" {
		return 0, false
	}
	fee, err := strconv.ParseFloat(feeStr, 64)
	if err != nil || fee == 0 {
		return 0, false
	}
	switch feeCcy {
	case "USD", "USDT", "USDC":
		return math.Abs(fee), true
	}
	return 0, false
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

// post 发带签名头的 POST JSON 请求并读回 body（非 2xx → 错误带响应体）。
// D-120：gate futures 创建订单返回 201 Created（非 200），HTTP 标准 2xx 都是成功，
// 业务层错误由各 venue 解析逻辑处理（OKX code!=0 / Binance code<0 / gate finish_as）。
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, ts+method+path+body))（与 reconcile 同法）。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// okxSwapSpec okx SWAP instruments 规格（ctVal=合约面值/lotSz=最小张数）。
type okxSwapSpec struct{ ctVal, lotSz float64 }

// okxSwapSpecCache instruments 规格包级缓存（ctVal/lotSz 不变）——
// D-311 R4：CEXExecutor 下单换算与 dashboard floorToStepQty 共用同源缓存。
var okxSwapSpecCache sync.Map // instID → okxSwapSpec

// OkxSwapSpec 查 okx swap 合约面值 ctVal + 最小张数 lotSz（D-311 R4 从 okxContractVal 提取共用）。
// 公开端点 GET /api/v5/public/instruments?instType=SWAP&instId=XXX 无需签名。
func OkxSwapSpec(ctx context.Context, client *http.Client, baseURL, instID string) (ctVal, lotSz float64, err error) {
	if v, ok := okxSwapSpecCache.Load(instID); ok {
		s := v.(okxSwapSpec)
		return s.ctVal, s.lotSz, nil
	}
	endpoint := "/api/v5/public/instruments?instType=SWAP&instId=" + instID
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+endpoint, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("okx instruments req: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("okx instruments %s: %w", instID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("okx instruments read: %w", err)
	}
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("okx instruments %s: http %d: %s", instID, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			CtVal string `json:"ctVal"`
			LotSz string `json:"lotSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, fmt.Errorf("okx instruments json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return 0, 0, fmt.Errorf("okx instruments %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	ctVal, err = strconv.ParseFloat(out.Data[0].CtVal, 64)
	if err != nil || ctVal <= 0 {
		return 0, 0, fmt.Errorf("okx instruments %s: ctVal=%q invalid", instID, out.Data[0].CtVal)
	}
	lotSz, err = strconv.ParseFloat(out.Data[0].LotSz, 64)
	if err != nil || lotSz <= 0 {
		return 0, 0, fmt.Errorf("okx instruments %s: lotSz=%q invalid", instID, out.Data[0].LotSz)
	}
	okxSwapSpecCache.Store(instID, okxSwapSpec{ctVal: ctVal, lotSz: lotSz})
	return ctVal, lotSz, nil
}

// okxContractVal 查 okx swap 合约面值 ctVal + 最小张数 lotSz。
// 公开端点 GET /api/v5/public/instruments?instType=SWAP&instId=XXX 无需签名。
// 缓存包级共享（D-311 R4，原 per-executor），避免每单查。D-125。
func (e *CEXExecutor) okxContractVal(ctx context.Context, instID string) (ctVal, lotSz float64, err error) {
	return OkxSwapSpec(ctx, e.client, e.baseURL, instID)
}
