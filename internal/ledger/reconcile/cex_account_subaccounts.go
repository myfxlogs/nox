// cex_account_subaccounts.go —— D-363 S2：CEX 资金（funding）/理财（earn）子账户查询。
// 只报 USDT 余额（D-364 v2 同语义：非 USDT 标不读取不统计，DOGE/USDC 等全跳）。
// 路由核证点④（9-20 真 key 实探留证）：binance sapi 走代理根路径（/api 前缀 404 实探）。
// 9-22 Devin 终席更正：okx 资金/理财端点「代理不路由（403）」系误诊——CF 1010 bot 拦截
// （无 UA 探针特征）被当成不路由；真签名实测经代理 200。直连使流量被本机 mihomo 分流到
// 轮换节点 → OKX 白名单 50110 刷屏。改回 config base_url（代理）优先、直连兜底。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	v1 "nox/internal/gen/dashboardv1"
)

// Account 子账户分派（D-363 S2）：venue_type=funding/earn 先于交易账户 venue 分派。
// gate 无 funding/earn 配置（仅 sim，种子不种）→ default 报错同 gate spot 先例。
func (q *CEXAccountQuerier) accountSubaccount(ctx context.Context) (*AccountInfo, error) {
	switch {
	case q.venueType == "funding":
		// binance 资金钱包官方废除并入现货+0182 配置行已删——binance 分支永久
		// 不可达同死码清退（funding venue_type 机制保留=okx:funding 在域）。
		if q.venue == "okx" {
			return q.accountOkxFunding(ctx)
		}
		return nil, fmt.Errorf("funding account: unsupported venue %q", q.venue)
	case q.venueType == "earn":
		// binance:earn 回归（财务可见性两件套①）：D-133 摘除令的「只统计 USDT」约束
		// 已被分币快照口径（D-285）解除——逐资产枚举落分币行。
		if q.venue == "okx" {
			return q.accountOkxEarn(ctx)
		}
		if q.venue == "binance" {
			return q.accountBinanceEarn(ctx)
		}
		if q.venue == "gate" { // D-523③：uni-lend 出借中=earn 池（availabilityGateEarn 同径）
			return q.accountGateEarn(ctx)
		}
		return nil, fmt.Errorf("earn account: unsupported venue %q", q.venue)
	}
	return nil, fmt.Errorf("subaccount: unsupported %s:%s", q.venue, q.venueType)
}

// okxSubaccountBase okx 资金/理财端点兜底基址：config base_url 为空才直连 www.okx.com。
// 9-22 更正（Devin 终席）：9-20「代理不路由 403」系 CF 1010 bot 拦截误诊，真签名实测代理
// 路由正常且代理 IP 在白名单内；直连会被主机代理分流到轮换出口 IP → 50110。包级变量保留
// 为测试 httptest 注入点与无 config 兜底。
var okxSubaccountBase = "https://www.okx.com"

// okxSubaccountSignedGet okx 子账户端点签名 GET（签名同 accountOKX：prehash=ts+method+path+query）。
// 路由：config base_url 非空走之（cex-proxy 固定 IP 白名单），空才回落直连兜底。
func (q *CEXAccountQuerier) okxSubaccountSignedGet(ctx context.Context, pathWithQuery string) ([]byte, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxSign(q.apiSecret, ts+"GET"+pathWithQuery)
	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	base := q.baseURL
	if base == "" {
		base = okxSubaccountBase
	}
	return q.get(ctx, base+pathWithQuery, headers)
}

// accountOkxFunding okx 资金账户：GET /api/v5/asset/balances?ccy=USDT → USDT 行 bal（真探
// 9-20：USDT 0=资金账户空）。
func (q *CEXAccountQuerier) accountOkxFunding(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/asset/balances?ccy=USDT")
	if err != nil {
		return nil, fmt.Errorf("okx funding: %w", err)
	}
	eq, holdings, err := parseOkxUSDTBalance(body, "bal")
	if err != nil {
		return nil, fmt.Errorf("okx funding json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// accountOkxEarn okx 理财（活期储蓄）：GET /api/v5/finance/savings/balance?ccy=USDT →
// USDT 行 amt（真探 9-20：amt=5261.25/rate=0.01/earnings=$69.64 在档）→ equity=amt。
func (q *CEXAccountQuerier) accountOkxEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/finance/savings/balance?ccy=USDT")
	if err != nil {
		return nil, fmt.Errorf("okx earn: %w", err)
	}
	eq, holdings, err := parseOkxUSDTBalance(body, "amt")
	if err != nil {
		return nil, fmt.Errorf("okx earn json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// parseOkxUSDTBalance okx 子账户响应（/asset/balances 与 /finance/savings/balance 同形：
// data[] 行含 ccy+数量字段）——只取 USDT 行数量字段 field 为 equity，holdings 仅 USDT 行。
func parseOkxUSDTBalance(body []byte, field string) (eq float64, holdings []*v1.Holding, err error) {
	var resp struct {
		Code string              `json:"code"`
		Msg  string              `json:"msg"`
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, nil, err
	}
	if resp.Code != "0" {
		return 0, nil, fmt.Errorf("code=%s msg=%s", resp.Code, resp.Msg)
	}
	for _, d := range resp.Data {
		if d["ccy"] != "USDT" {
			continue
		}
		v, perr := strconv.ParseFloat(d[field], 64)
		if perr != nil {
			continue
		}
		eq = v
		if v > 0 {
			holdings = append(holdings, &v1.Holding{Ccy: "USDT", Qty: v, UsdtValue: v})
		}
		break
	}
	return eq, holdings, nil
}

// gateUniLendRow /earn/uni/lends 行面（venue 实拍 10-15：真实字段 amount/lent_amount/
// frozen_amount——current_amount 系误字段恒空）。lent_amount=出借中本金。
type gateUniLendRow struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Lent     string `json:"lent_amount"`
	Frozen   string `json:"frozen_amount"`
}

// parseGateUniLends lends 响应→分币持仓（D-285 零换算；equity=USDT 出借额）。
func parseGateUniLends(body []byte) (usdt float64, holdings []*v1.Holding, err error) {
	rows, err := parseGateUniLendRows(body)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range rows {
		v, perr := strconv.ParseFloat(r.Lent, 64)
		if perr != nil || v <= 0 || r.Currency == "" {
			continue
		}
		uv := 0.0
		if r.Currency == "USDT" {
			uv = v
			usdt += v
		}
		holdings = append(holdings, &v1.Holding{Ccy: r.Currency, Qty: v, UsdtValue: uv})
	}
	return usdt, holdings, nil
}

// accountGateEarn gate 理财（uni-lend 出借中）：GET /earn/uni/lends 签名读——
// D-523③ 接 rest_poll 快照写口（gate:earn 键此前无写径，venue 实有出借零可见）。
func (q *CEXAccountQuerier) accountGateEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.gateSignedGet(ctx, "/earn/uni/lends")
	if err != nil {
		return nil, fmt.Errorf("gate earn: %w", err)
	}
	usdt, holdings, err := parseGateUniLends(body)
	if err != nil {
		return nil, fmt.Errorf("gate earn json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: usdt, Equity: usdt, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}
