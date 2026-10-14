// earn_rail_gate.go —— gate earn/uni lend-redeem rail（earn_rail.go 拆出件）。
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

// ---- gate rail：earn/uni lend-redeem ----

type gateEarnRail struct {
	e *Executor
	c EarnCred
}

func (r *gateEarnRail) req(ctx context.Context, method, endpoint, query, body string) ([]byte, error) {
	urlPath := "/api/v4" + endpoint
	ts := r.e.Now().Unix()
	url := gateAPIBase(r.c.BaseURL) + urlPath
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

// gateAPIBase base_url→API host 基址：库内约定已含 /api/v4 前缀（reconcile 实证）——
// 剥后缀防双拼（旧径 …/api/v4/api/v4/… → venue HTML 404，SG #377 实证）；空→原生域。
func gateAPIBase(raw string) string {
	base := strings.TrimSuffix(strings.TrimRight(raw, "/"), "/api/v4")
	if base == "" {
		return "https://api.gateio.ws"
	}
	return base
}
