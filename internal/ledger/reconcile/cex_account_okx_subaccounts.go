// cex_account_okx_subaccounts.go —— D-363 S2：okx funding/earn 子账户查询实现
// （自 cex_account_subaccounts.go 按 venue 搬出——check-lines 拆分，零语义变化）。
// 9-22 Devin 终席更正：okx 资金/理财端点「代理不路由（403）」系误诊——CF 1010 bot 拦截
// （无 UA 探针特征）被当成不路由；真签名实测经代理 200。直连使流量被本机 mihomo 分流到
// 轮换节点 → OKX 白名单 50110 刷屏。config base_url（代理）优先、直连兜底。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	v1 "nox/internal/gen/dashboardv1"
)

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
	// D-537：sfp 结构化产品通配读（dcd 之外的 structured 持仓——10 USDT 在仓
	// 快照零行业主实拍）。端点未证实枚举全产品域 → 读败仅记日志不杀 earn 主读面
	// （漏采由残差对账告警自披露）；dcd 已覆盖的子集幂等去重按 ordId。
	sfpRefs, serr := q.okxSfpOrders(ctx, func(c string, v float64) {
		if _, ok := per[c]; !ok {
			ccys = append(ccys, c)
		}
		per[c] += v
	})
	if serr != nil {
		log.Printf("[okx earn] sfp structured orders 读败（该域漏采将对账告警暴露）: %v", serr)
	}
	dciRefs = append(dciRefs, sfpRefs...)
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

// okxSfpOrders D-537：sfp 结构化产品在仓单——GET /api/v5/finance/sfp/orders
// （dcd 专径之外的通配面；响应行复用 dcd 字段形 notionalCcy/investCcy+
// notionalSz/investAmt）。state∈{live,pending_settle} 才入（SETTLED 等终态排除，
// 空 state fail-closed 报错不过滤——binance DCI purchaseStatus 教训复用）。
func (q *CEXAccountQuerier) okxSfpOrders(ctx context.Context, add func(string, float64)) ([]string, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/finance/sfp/orders")
	if err != nil {
		return nil, fmt.Errorf("okx sfp orders: %w", err)
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
		return nil, fmt.Errorf("okx sfp orders json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx sfp orders: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var refs []string
	for _, r := range resp.Data {
		if r.State == "" {
			return nil, fmt.Errorf("okx sfp orders %s 行 state 空（fail-closed 不过滤）", r.OrdID)
		}
		if r.State != "live" && r.State != "pending_settle" {
			continue
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
			return nil, fmt.Errorf("okx sfp orders %s 行 ccy/amount 字段缺", r.OrdID)
		}
		qty, perr := strconv.ParseFloat(amt, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse okx sfp %s amount=%q: %w", r.OrdID, amt, perr)
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
