// earn_rail.go —— earn 执行径多 venue 化：产品读/申购/赎回/余额四动词 per venue
// 适配面。binance=原实现包壳零行为变；okx=savings purchase-redemption；gate=earn/uni
// lend-redeem。凭据源=exchange_configs（装配注入 EarnCreds，缺行=nil rail→credential
// 缺口不臆造）。余额口径：okx=unified availBal、gate=现货 available、binance=spot free。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"nox/internal/platform/signutil"
)

// EarnCred venue 凭据（exchange_configs 行投影；装配注入）。
type EarnCred struct {
	Key     string
	Secret  string
	Pass    string // okx passphrase；binance/gate 不用
	BaseURL string
}

// EarnRail earn 执行适配面（三动词+余额读；测试注 EarnRails 替真发）。
type EarnRail interface {
	Products(ctx context.Context, ccy string) ([]EarnProduct, error)
	Subscribe(ctx context.Context, productID string, amount float64) (string, error)
	Redeem(ctx context.Context, productID string, amount float64) error
	FreeBalance(ctx context.Context, ccy string) (float64, error)
}

// earnRail venue→rail 分派：测试注 EarnRails 优先，否则按 EarnCreds 建真件。
func (e *Executor) earnRail(venue string) (EarnRail, bool) {
	if e.EarnRails != nil {
		r, ok := e.EarnRails[venue]
		return r, ok
	}
	if venue == "binance" {
		// 旧字段凭据/测试注口在位即建 rail（EarnCreds 未填兼容——BnKey/BnSec/
		// EarnProductsFn/EarnSubscribeFn 是 binance 径存量注入面）。
		if e.BnKey != "" || e.EarnProductsFn != nil || e.EarnSubscribeFn != nil {
			return &binanceEarnRail{e: e}, true
		}
		return nil, false
	}
	c, ok := e.EarnCreds[venue]
	if !ok || c.Key == "" || c.Secret == "" {
		return nil, false
	}
	switch venue {
	case "okx":
		return &okxEarnRail{e: e, c: c}, true
	case "gate":
		return &gateEarnRail{e: e, c: c}, true
	}
	return nil, false
}

// binanceEarnRail 原径包壳（行为零变——真 HTTP 走既有 earnProducts/earnSubscribe/
// freeBalance/redeemFlexible 方法体）。
type binanceEarnRail struct{ e *Executor }

func (r *binanceEarnRail) Products(ctx context.Context, ccy string) ([]EarnProduct, error) {
	return r.e.earnProducts(ctx, ccy)
}
func (r *binanceEarnRail) Subscribe(ctx context.Context, productID string, amount float64) (string, error) {
	return r.e.earnSubscribe(ctx, productID, amount)
}
func (r *binanceEarnRail) Redeem(ctx context.Context, productID string, amount float64) error {
	return r.e.redeemFlexible(ctx, productID, amount)
}
func (r *binanceEarnRail) FreeBalance(ctx context.Context, ccy string) (float64, error) {
	return r.e.freeBalance(ctx, ccy)
}

// ---- okx rail：savings purchase-redemption 双端点 ----

type okxEarnRail struct {
	e *Executor
	c EarnCred
}

func (r *okxEarnRail) req(ctx context.Context, method, pathWithQuery, body string) ([]byte, error) {
	ts := strconv.FormatFloat(float64(r.e.Now().UnixMilli())/1000, 'f', 3, 64)
	url := strings.TrimRight(r.c.BaseURL, "/") + pathWithQuery
	if r.c.BaseURL == "" {
		url = "https://www.okx.com" + pathWithQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("OK-ACCESS-KEY", r.c.Key)
	req.Header.Set("OK-ACCESS-SIGN", signutil.OkxSign(r.c.Secret, ts, method, pathWithQuery, body))
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", r.c.Pass)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.e.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("okx %s HTTP %d: %s", pathWithQuery, resp.StatusCode, string(b))
	}
	var env struct {
		Code string          `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("okx %s 解析: %w", pathWithQuery, err)
	}
	if env.Code != "" && env.Code != "0" {
		return nil, fmt.Errorf("okx %s code=%s: %s", pathWithQuery, env.Code, env.Msg)
	}
	return env.Data, nil
}

// Products okx 活期=余币宝：`GET /finance/savings/lending-rate-summary` 行
// {ccy, estRate/preRate}——estRate 估利率优先；APR 字段全缺→不可购 fail-closed。
func (r *okxEarnRail) Products(ctx context.Context, ccy string) ([]EarnProduct, error) {
	data, err := r.req(ctx, http.MethodGet,
		"/api/v5/finance/savings/lending-rate-summary?ccy="+ccy, "")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Ccy     string `json:"ccy"`
		EstRate string `json:"estRate"`
		PreRate string `json:"preRate"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("okx lending-rate-summary 解析: %w", err)
	}
	var out []EarnProduct
	for _, row := range rows {
		if row.Ccy != ccy {
			continue
		}
		apr := parseF(row.EstRate)
		if apr <= 0 {
			apr = parseF(row.PreRate) // estRate 缺→preRate 前次成交率如实回落
		}
		out = append(out, EarnProduct{
			ProductID:   "okx-saving-" + ccy,
			Asset:       ccy,
			APR:         apr,
			CanPurchase: apr > 0,
		})
	}
	return out, nil
}

// Subscribe 申购前置划转+申购：savings 只认资金账户资产（venue 文档实证）——
// 先读 funding 现额算短差 needed=amt−funding；needed≤0 零划转直购，
// needed>0 划 `min(needed, unified_avail)`（unified 无可划=如实拒不试申购）。
// 端点翻案（SG 10-14 探针实证）：文档截断形 `purchase-redempt` 才是真路径——
// OKX 网关对签名未命中路径统一回 403 伪装权限拒，非权限病灶。
func (r *okxEarnRail) Subscribe(ctx context.Context, _ string, amount float64) (string, error) {
	funding, err := r.fundingAvail(ctx, "USDT")
	if err != nil {
		return "", err
	}
	if needed := amount - funding; needed > 0 {
		unified, err := r.unifiedAvail(ctx, "USDT")
		if err != nil {
			return "", err
		}
		// 短差须足额盖——unified<needed 划过去申购照样败（尘额划转 SG #22 实证
		// venue 400 Parameter amt error）：unified 盖不住短差=fail-closed 不申购。
		if unified < needed {
			return "", fmt.Errorf("okx 申购短差 %.8f、unified 可动 %.8f 盖不住——fail-closed 不申购", needed, unified)
		}
		tb := fmt.Sprintf(`{"ccy":"USDT","amt":"%s","from":"18","to":"6","type":"0"}`,
			strconv.FormatFloat(needed, 'f', 8, 64))
		if _, err := r.req(ctx, http.MethodPost, "/api/v5/asset/transfer", tb); err != nil {
			return "", fmt.Errorf("okx 18→6 划转败（不试申购）: %w", err)
		}
	}
	body := fmt.Sprintf(`{"ccy":"USDT","amt":"%s","side":"purchase"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	b, err := r.req(ctx, http.MethodPost, "/api/v5/finance/savings/purchase-redempt", body)
	return string(b), err
}

func (r *okxEarnRail) Redeem(ctx context.Context, _ string, amount float64) error {
	body := fmt.Sprintf(`{"ccy":"USDT","amt":"%s","side":"redempt"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	_, err := r.req(ctx, http.MethodPost, "/api/v5/finance/savings/purchase-redempt", body)
	return err
}

// FreeBalance 可动=unified 交易户 availBal + funding 资金户 availBal 合并口径
// （两池都可经划转达申购；SG 实证自划转后 funding=1958 单读 unified 报 0 假贫）。
func (r *okxEarnRail) FreeBalance(ctx context.Context, ccy string) (float64, error) {
	u, err := r.unifiedAvail(ctx, ccy)
	if err != nil {
		return 0, err
	}
	f, err := r.fundingAvail(ctx, ccy)
	if err != nil {
		return 0, err
	}
	return u + f, nil
}

// unifiedAvail `GET /account/balance?ccy=` → data[].details[].availBal（交易户）。
func (r *okxEarnRail) unifiedAvail(ctx context.Context, ccy string) (float64, error) {
	data, err := r.req(ctx, http.MethodGet, "/api/v5/account/balance?ccy="+ccy, "")
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Details []struct {
			Ccy      string `json:"ccy"`
			AvailBal string `json:"availBal"`
		} `json:"details"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return 0, fmt.Errorf("okx balance 解析: %w", err)
	}
	for _, row := range rows {
		for _, d := range row.Details {
			if d.Ccy == ccy {
				return parseF(d.AvailBal), nil
			}
		}
	}
	return 0, nil // 币种行缺席=该币零可动（venue 实响应零值如实）
}

// fundingAvail `GET /asset/balances?ccy=` → data[].availBal（资金户）。
// SG 10-14 探针实证：单数形 /asset/balance=404 未命中——复数 balances 才是真路径。
func (r *okxEarnRail) fundingAvail(ctx context.Context, ccy string) (float64, error) {
	data, err := r.req(ctx, http.MethodGet, "/api/v5/asset/balances?ccy="+ccy, "")
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Ccy      string `json:"ccy"`
		AvailBal string `json:"availBal"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return 0, fmt.Errorf("okx asset balance 解析: %w", err)
	}
	for _, row := range rows {
		if row.Ccy == ccy {
			return parseF(row.AvailBal), nil
		}
	}
	return 0, nil
}
