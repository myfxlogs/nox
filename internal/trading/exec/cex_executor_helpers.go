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
	"net/http"
	"net/url"
	"strconv"
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

// OKXPosition 是一条 OKX 持仓（GET /api/v5/account/positions，D-059 实施修正② 自愈清理用）。
type OKXPosition struct {
	InstID  string
	PosSide string // long / short / net
	Pos     float64
	AvgPx   float64
}

// okxPosModeFetch D-564 S6 U8：GET /api/v5/account/config → posMode 实读
// （long_short_mode/net_mode…）。禁缓存（业主 app 可改模式，缓存=快照进决策违禁）。
// 走 okxGetSignedRetry（限频退避 ≤2 次 + 50102 校时重发）。
func (e *CEXExecutor) okxPosModeFetch(ctx context.Context) (string, error) {
	path := "/api/v5/account/config"
	body, err := e.okxGetSignedRetry(ctx, path)
	if err != nil {
		return "", fmt.Errorf("okx account/config: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			PosMode string `json:"posMode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("okx account/config json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return "", fmt.Errorf("okx account/config code=%s data_len=%d", resp.Code, len(resp.Data))
	}
	if resp.Data[0].PosMode == "" {
		return "", fmt.Errorf("okx account/config posMode 空")
	}
	return resp.Data[0].PosMode, nil
}

// okxPosModeCheck D-564 S6 U8：posMode 实读+校验（非 long_short_mode 一律拒+报实测值）。
// placeOKX perp 分支调用；判定时刻实读禁缓存。
func (e *CEXExecutor) okxPosModeCheck(ctx context.Context) error {
	// singleflight 在途合并——并发多单共享同次实读，禁时效缓存（判定时刻实读语义不变）。
	v, err, _ := e.posModeSF.Do("posmode", func() (any, error) {
		return e.okxPosModeFetch(ctx)
	})
	if err != nil {
		return fmt.Errorf("okx posMode 实读 fail-closed: %w", err)
	}
	posMode := v.(string)
	if posMode != "long_short_mode" {
		return fmt.Errorf("okx 账户模式已变更（posMode=%s），拒绝按 long/short 语义下单（fail-closed；net_mode 映射未做）", posMode)
	}
	return nil
}

// OKXPositions 查询 OKX 指定 instId 的当前持仓（long_short_mode 下 long/short 分列）。
// 供平仓核对 / 残留双向持仓清理，签名四头。
func (e *CEXExecutor) OKXPositions(ctx context.Context, instID string) ([]OKXPosition, error) {
	path := "/api/v5/account/positions?instId=" + url.QueryEscape(instID)
	ts := e.okxNowISO() // D-566 S3：校时感知 ts
	prehash := ts + "GET" + path
	sign := okxSign(e.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
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
		p, err := f64z(d.Pos)
		if err != nil {
			return nil, fmt.Errorf("okx positions %s pos=%q: %w", d.InstID, d.Pos, err)
		}
		if p == 0 {
			continue
		}
		a, err := f64z(d.AvgPx)
		if err != nil {
			return nil, fmt.Errorf("okx positions %s avgPx=%q: %w", d.InstID, d.AvgPx, err)
		}
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
		return nil, &httpStatusError{status: resp.StatusCode, body: data} // D-566：类型化（消息不变）
	}
	return data, nil
}

// post 发带签名头的 POST JSON 请求并读回 body（非 2xx → 错误带响应体）。
// gate futures 创建订单返回 201 Created（非 200），HTTP 标准 2xx 都是成功，
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
		return nil, &httpStatusError{status: resp.StatusCode, body: data} // D-566：类型化（消息不变）
	}
	return data, nil
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, ts+method+path+body))（与 reconcile 同法）。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// f64z 交易所字符串数值解析（D-508 S2）：""=字段缺席→0（venue 对未成数字段返空串是
// 正常语义，如 okx 未成交单 accFillSz=""）；非空脏串 error 上抛不静默归零。
func f64z(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
