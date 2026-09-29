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

// OKXPosition 是一条 OKX 持仓（GET /api/v5/account/positions，D-059 实施修正② 自愈清理用）。
type OKXPosition struct {
	InstID  string
	PosSide string // long / short / net
	Pos     float64
	AvgPx   float64
}

// okxPosModeFetch D-564 S6 U8：GET /api/v5/account/config → posMode 实读
// （long_short_mode/net_mode…）。禁缓存（业主 app 可改模式，缓存=快照进决策违禁）。
func (e *CEXExecutor) okxPosModeFetch(ctx context.Context) (string, error) {
	path := "/api/v5/account/config"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
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
	posMode, err := e.okxPosModeFetch(ctx)
	if err != nil {
		return fmt.Errorf("okx posMode 实读 fail-closed: %w", err)
	}
	if posMode != "long_short_mode" {
		return fmt.Errorf("okx 账户模式已变更（posMode=%s），拒绝按 long/short 语义下单（fail-closed；net_mode 映射未做）", posMode)
	}
	return nil
}

// okxStateCheck D-564 S6 U9：instruments.state 闸——非 live（preopen/suspend/expired）
// fail-closed。spec 缓存共享（okxSwapSpecCache 同包），state 随缓存写（规格慢变元数据）。
func (e *CEXExecutor) okxStateCheck(ctx context.Context, instID string) error {
	if v, ok := okxSwapSpecState.Load(instID); ok {
		if st, _ := v.(string); st != "" && st != "live" {
			return fmt.Errorf("okx %s 合约非 live 态（state=%s）fail-closed 拒单", instID, st)
		}
		return nil
	}
	endpoint := "/api/v5/public/instruments?instType=SWAP&instId=" + instID
	body, err := e.get(ctx, e.baseURL+endpoint, nil)
	if err != nil {
		return fmt.Errorf("okx instruments state %s: %w", instID, err)
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("okx instruments state json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return fmt.Errorf("okx instruments state %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	st := out.Data[0].State
	if st == "" {
		return nil // 老响应无 state 字段=兼容直通（不猜不拒——字段缺席非证据）
	}
	if st != "live" {
		// 非 live 不缓存（preopen→live 转换后下次尝试重查；缓存 live 正常态）。
		return fmt.Errorf("okx %s 合约非 live 态（state=%s）fail-closed 拒单", instID, st)
	}
	okxSwapSpecState.Store(instID, st)
	return nil
}

// OKXPositions 查询 OKX 指定 instId 的当前持仓（long_short_mode 下 long/short 分列）。
// 供平仓核对 / 残留双向持仓清理，签名四头。
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

// okxSwapSpecState D-564 S6 U9：instruments.state 缓存（live/preopen/suspend/expired；
// 规格慢变元数据允许缓存——state 闸非决策时变数据面）。
var okxSwapSpecState sync.Map

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
	ctVal, err = f64z(out.Data[0].CtVal)
	if err != nil || ctVal <= 0 {
		return 0, 0, fmt.Errorf("okx instruments %s: ctVal=%q invalid", instID, out.Data[0].CtVal)
	}
	lotSz, err = f64z(out.Data[0].LotSz)
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

// okxSpotLotSz D-473（KB okx-perp-ctval-qty-conversion 同型修）：spot instruments
// 的 lotSz/minSz（instType=SPOT，sz 单位=基础币）。取不到→err fail-closed
// （不下未地板单——51121/最小金额拒单先例）。
func (e *CEXExecutor) okxSpotLotSz(ctx context.Context, instID string) (lotSz, minSz float64, err error) {
	if s, ok := okxSpotSpecCache.Load(instID); ok {
		sp := s.(okxSpotSpec)
		return sp.lotSz, sp.minSz, nil
	}
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/instruments?instType=SPOT&instId="+url.QueryEscape(instID), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("okx spot instruments %s: %w", instID, err)
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			LotSz string `json:"lotSz"`
			MinSz string `json:"minSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, fmt.Errorf("okx spot instruments json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return 0, 0, fmt.Errorf("okx spot instruments %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	lotSz, err = f64z(out.Data[0].LotSz)
	if err != nil || lotSz <= 0 {
		return 0, 0, fmt.Errorf("okx spot instruments %s: lotSz=%q invalid", instID, out.Data[0].LotSz)
	}
	if minSz, err = f64z(out.Data[0].MinSz); err != nil {
		return 0, 0, fmt.Errorf("okx spot instruments %s: minSz=%q invalid: %w", instID, out.Data[0].MinSz, err)
	}
	okxSpotSpecCache.Store(instID, okxSpotSpec{lotSz: lotSz, minSz: minSz})
	return lotSz, minSz, nil
}

type okxSpotSpec struct{ lotSz, minSz float64 }

var okxSpotSpecCache sync.Map

// f64z 交易所字符串数值解析（D-508 S2）：""=字段缺席→0（venue 对未成数字段返空串是
// 正常语义，如 okx 未成交单 accFillSz=""）；非空脏串 error 上抛不静默归零。
func f64z(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
