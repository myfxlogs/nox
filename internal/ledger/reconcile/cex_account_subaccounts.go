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

// accountOkxFunding okx 资金账户：GET /api/v5/asset/balances 全币枚举（D-528 同型
// 病灶修——旧 ?ccy=USDT 过滤使资金户非 USDT 持仓零账面；该端点无估值字段，
// 分币行原生量零换算，聚合 equity 仍=ΣUSDT.bal 旧口径）。
func (q *CEXAccountQuerier) accountOkxFunding(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/asset/balances")
	if err != nil {
		return nil, fmt.Errorf("okx funding: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Ccy   string `json:"ccy"`
			Bal   string `json:"bal"`
			Avail string `json:"availBal"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx funding json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx funding: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var eq float64
	var holdings []*v1.Holding
	freeByCcy := map[string]float64{}
	for _, d := range resp.Data {
		v, perr := strconv.ParseFloat(d.Bal, 64)
		if perr != nil || v <= 0 {
			continue
		}
		uv := 0.0
		if d.Ccy == "USDT" {
			uv = v
			eq += v
		}
		holdings = append(holdings, &v1.Holding{Ccy: d.Ccy, Qty: v, UsdtValue: uv})
		if a, perr := strconv.ParseFloat(d.Avail, 64); perr == nil {
			freeByCcy[d.Ccy] += a
		}
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT", FreeMargin: freeByCcy["USDT"],
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
		FreeQtyByCcy: freeByCcy,
	}, nil
}

// accountOkxEarn okx 理财：savings 余额（USDT 行 amt）+ DCI（dcd）在仓单并入。
// DCI 同型病灶修（binance 漏 $358 先例）——okx 双币仓位走 sfp/dcd 独立产品域
// 不在 savings 读面；在仓单本金按 notionalCcy/investCcy 归位分币行。
// savings 端点读败 fail-closed；dcd/orders 读败同规（如实缺不编）。
func (q *CEXAccountQuerier) accountOkxEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/finance/savings/balance?ccy=USDT")
	if err != nil {
		return nil, fmt.Errorf("okx earn: %w", err)
	}
	_, holdings, err := parseOkxUSDTBalance(body, "amt")
	if err != nil {
		return nil, fmt.Errorf("okx earn json: %w", err)
	}
	per := map[string]float64{}
	var ccys []string
	for _, h := range holdings {
		per[h.Ccy] += h.Qty
		ccys = append(ccys, h.Ccy)
	}
	dciRefs, err := q.okxDCIOrders(ctx, func(c string, v float64) {
		if _, ok := per[c]; !ok {
			ccys = append(ccys, c)
		}
		per[c] += v
	})
	if err != nil {
		return nil, err
	}
	eq := 0.0
	holdings = holdings[:0]
	for _, c := range ccys {
		eq += per[c]
		uv := 0.0
		if c == "USDT" {
			uv = per[c]
		}
		holdings = append(holdings, &v1.Holding{Ccy: c, Qty: per[c], UsdtValue: uv})
	}
	ccy := "USDT"
	if len(ccys) == 1 {
		ccy = ccys[0]
	} else if len(ccys) > 1 {
		ccy = "MULTI"
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: ccy,
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
		DCIOpenRefs: dciRefs,
	}, nil
}

// okxDCIOrders okx 双币（DCI）在仓单读：GET /api/v5/finance/sfp/dcd/orders
// （在仓单列表端点；order-status 需逐单 ordId 不适用快照面）。state 非
// live/pending 行不入；本金键 notionalSz/investAmt、币种键 notionalCcy/investCcy
// 多形容错（venue 实拍为准）；行缺 ccy/amt fail-closed 返错。
// 返回在仓单引用全集（ordId/productId——台账对拍锚）。
func (q *CEXAccountQuerier) okxDCIOrders(ctx context.Context, add func(string, float64)) ([]string, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/finance/sfp/dcd/orders")
	if err != nil {
		return nil, fmt.Errorf("okx dcd orders: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID     string `json:"ordId"`
			ProductID string `json:"productId"`
			State     string `json:"state"`
			Ccy       string `json:"notionalCcy"`
			CcyAlt    string `json:"investCcy"`
			Amt       string `json:"notionalSz"`
			AmtAlt    string `json:"investAmt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx dcd orders json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx dcd orders: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var refs []string
	for _, r := range resp.Data {
		if r.State != "" && r.State != "live" && r.State != "pending_settle" {
			continue // 终态/撤单行不入 earn 池、不入对拍锚集
		}
		for _, ref := range []string{r.OrdID, r.ProductID} {
			if ref != "" {
				refs = append(refs, ref)
			}
		}
		ccy := r.Ccy
		if ccy == "" {
			ccy = r.CcyAlt
		}
		amt := r.Amt
		if amt == "" {
			amt = r.AmtAlt
		}
		if ccy == "" || amt == "" {
			return nil, fmt.Errorf("okx dcd orders %s 行 ccy/amount 字段缺（notionalCcy/notionalSz 双键皆空）", r.OrdID)
		}
		qty, perr := strconv.ParseFloat(amt, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse okx dcd %s amount=%q: %w", r.OrdID, amt, perr)
		}
		if qty > 0 {
			add(ccy, qty)
		}
	}
	return refs, nil
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
