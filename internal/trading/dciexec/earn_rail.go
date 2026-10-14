// earn_rail.go —— earn 执行径多 venue 化：产品读/申购/赎回/余额四动词 per venue
// 适配面。binance=原实现包壳零行为变；okx=savings purchase-redemption；gate=earn/uni
// lend-redeem。凭据源=exchange_configs（装配注入 EarnCreds，缺行=nil rail→credential
// 缺口不臆造）。余额口径：okx=unified availBal、gate=perp available、binance=spot free。
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

// Subscribe 余币宝申购：`POST /finance/savings/purchase-redemption`
// {ccy,amt,side:purchase}——rate 不带（venue 按市价撮合，带 rate 会拒超额；产品读已验 APR>0）。
func (r *okxEarnRail) Subscribe(ctx context.Context, _ string, amount float64) (string, error) {
	body := fmt.Sprintf(`{"ccy":"USDT","amt":"%s","side":"purchase"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	b, err := r.req(ctx, http.MethodPost, "/api/v5/finance/savings/purchase-redemption", body)
	return string(b), err
}

func (r *okxEarnRail) Redeem(ctx context.Context, _ string, amount float64) error {
	body := fmt.Sprintf(`{"ccy":"USDT","amt":"%s","side":"redempt"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	_, err := r.req(ctx, http.MethodPost, "/api/v5/finance/savings/purchase-redemption", body)
	return err
}

// FreeBalance unified 交易户可动：`GET /account/balance?ccy=` → data[].details[].availBal。
func (r *okxEarnRail) FreeBalance(ctx context.Context, ccy string) (float64, error) {
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

// ---- gate rail：earn/uni lend-redeem ----

type gateEarnRail struct {
	e *Executor
	c EarnCred
}

func (r *gateEarnRail) req(ctx context.Context, method, endpoint, query, body string) ([]byte, error) {
	urlPath := "/api/v4" + endpoint
	ts := r.e.Now().Unix()
	base := strings.TrimRight(r.c.BaseURL, "/")
	if base == "" {
		base = "https://api.gateio.ws"
	}
	url := base + urlPath
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("KEY", r.c.Key)
	req.Header.Set("SIGN", signutil.GateSign(r.c.Secret, method, urlPath, query, body, ts))
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
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
		return nil, fmt.Errorf("gate %s HTTP %d: %s", endpoint, resp.StatusCode, string(b))
	}
	return b, nil
}

// Products gate uni 借出币种详情 `GET /earn/uni/currencies/{ccy}`——
// {min_lend_amount, max_rate(hourly), min_rate}；max_rate>0 且 min 可解析=可购；
// APR=min_rate(hourly)×24×365 保守口径（max_rate 顶帽不拿来做广告值）。
func (r *gateEarnRail) Products(ctx context.Context, ccy string) ([]EarnProduct, error) {
	b, err := r.req(ctx, http.MethodGet, "/earn/uni/currencies/"+ccy, "", "")
	if err != nil {
		return nil, err
	}
	var row struct {
		Currency string `json:"currency"`
		MinLend  string `json:"min_lend_amount"`
		MaxRate  string `json:"max_rate"`
		MinRate  string `json:"min_rate"`
	}
	if err := json.Unmarshal(b, &row); err != nil {
		return nil, fmt.Errorf("gate uni currency 解析: %w", err)
	}
	minLend, maxRate, minRate := parseF(row.MinLend), parseF(row.MaxRate), parseF(row.MinRate)
	return []EarnProduct{{
		ProductID:   "gate-uni-" + ccy,
		Asset:       ccy,
		APR:         minRate * 24 * 365,
		CanPurchase: maxRate > 0 && minLend >= 0 && row.MinLend != "",
		MinPurchase: minLend,
	}}, nil
}

// Subscribe uni 借出 `POST /earn/uni/lends` {currency,amount,type:"lend"}——
// min_rate 不带（ venue 按当前率撮合；currency 面已读 min/max）。
func (r *gateEarnRail) Subscribe(ctx context.Context, _ string, amount float64) (string, error) {
	body := fmt.Sprintf(`{"currency":"USDT","amount":"%s","type":"lend"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	b, err := r.req(ctx, http.MethodPost, "/earn/uni/lends", "", body)
	return string(b), err
}

func (r *gateEarnRail) Redeem(ctx context.Context, _ string, amount float64) error {
	body := fmt.Sprintf(`{"currency":"USDT","amount":"%s","type":"redeem"}`,
		strconv.FormatFloat(amount, 'f', 8, 64))
	_, err := r.req(ctx, http.MethodPost, "/earn/uni/lends", "", body)
	return err
}

// FreeBalance perp 户可动 `GET /futures/usdt/accounts`→available（USDT 本位单币）。
func (r *gateEarnRail) FreeBalance(ctx context.Context, _ string) (float64, error) {
	b, err := r.req(ctx, http.MethodGet, "/futures/usdt/accounts", "", "")
	if err != nil {
		return 0, err
	}
	var row struct {
		Available string `json:"available"`
	}
	if err := json.Unmarshal(b, &row); err != nil {
		return 0, fmt.Errorf("gate futures accounts 解析: %w", err)
	}
	return parseF(row.Available), nil
}

func parseF(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
