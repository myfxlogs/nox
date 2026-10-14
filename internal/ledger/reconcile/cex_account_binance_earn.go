// cex_account_binance_earn.go —— binance 理财（simple-earn 活期）子账户查询。
// 财务可见性两件套①（D-133 摘除令的口径解除）：旧约束「理财面只统计 USDT」不可达已废——
// flexible/position 逐资产枚举，各币种分开落快照零换算（D-285）。
// 端点：GET {sapi}/v1/simple-earn/flexible/position（USER_DATA 签名）——
// 私有流不推 earn 仓位（outboundAccountPosition 只含现货钱包，业主令票文已核实），REST 唯一径。
// 路由核证点④先例：sapi 段经 BinanceApiSegBase 派生（代理根路径 /api 前缀 404 实探）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/httpx"
)

// accountBinanceEarn binance 活期理财：positions 逐资产行 → 单币 Equity=Σqty、
// 多币 Currency="MULTI"（ Equity=Σqty 仅作变更指纹，真值在快照分币行）。
func (q *CEXAccountQuerier) accountBinanceEarn(ctx context.Context) (*AccountInfo, error) {
	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey)
	base := httpx.BinanceApiSegBase(q.baseURL, "sapi")
	// SG 实证（2026-10-14）：活期 USDT 三单申购在账但 earn 池零行——flexible
	// 缺省分页（current=1,size=10）尘币占满首页把 USDT 挤出窗；另申购品可为
	// locked 定期（USDT001 形）归 locked/position 端点。双端点各翻页全采。
	per := map[string]float64{}
	var ccys []string
	add := func(asset string, qty float64) {
		if _, ok := per[asset]; !ok {
			ccys = append(ccys, asset)
		}
		per[asset] += qty
	}
	for _, seg := range []string{"flexible", "locked"} {
		if err := q.earnPositions(ctx, base, h, seg, add); err != nil {
			return nil, err
		}
	}
	equity, ccy := 0.0, ""
	var holdings []*v1.Holding
	for _, c := range ccys {
		equity += per[c]
		holdings = append(holdings, &v1.Holding{Ccy: c, Qty: per[c]})
	}
	switch len(ccys) {
	case 0:
		ccy = "USDT" // 空仓如实零权益，币种列占位沿用本位
	case 1:
		ccy = ccys[0]
	default:
		ccy = "MULTI"
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: equity, Equity: equity, Currency: ccy,
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// earnPositions simple-earn 仓位端点翻页全采（size=100，页内行<size 或累计≥total 止）。
// flexible/locked 同构行（locked 行额字段名 amount，flexible=totalAmount——双键兼容解析）。
// 端点败/解析败 fail-closed 返错——禁把「查不到=零仓位」静默吞。
func (q *CEXAccountQuerier) earnPositions(ctx context.Context, base string, h http.Header,
	seg string, add func(string, float64)) error {
	for current := 1; ; current++ {
		params := url.Values{}
		params.Set("current", strconv.Itoa(current))
		params.Set("size", "100")
		params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
		params.Set("recvWindow", "5000")
		params.Set("signature", binanceSign(q.apiSecret, params.Encode()))
		body, err := q.get(ctx, base+"/v1/simple-earn/"+seg+"/position?"+params.Encode(), h)
		if err != nil {
			return fmt.Errorf("binance earn %s position: %w", seg, err)
		}
		var resp struct {
			Total int `json:"total"`
			Rows  []struct {
				Asset       string `json:"asset"`
				TotalAmount string `json:"totalAmount"`
				Amount      string `json:"amount"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return fmt.Errorf("binance earn %s json: %w", seg, err)
		}
		var seen int
		for _, r := range resp.Rows {
			raw := r.TotalAmount
			if raw == "" {
				raw = r.Amount
			}
			if raw == "" {
				return fmt.Errorf("binance earn %s %s 行额字段双缺", seg, r.Asset)
			}
			qty, perr := strconv.ParseFloat(raw, 64)
			if perr != nil {
				return fmt.Errorf("parse %s %s amount=%q: %w", seg, r.Asset, raw, perr)
			}
			seen++
			if qty > 0 {
				add(r.Asset, qty)
			}
		}
		if len(resp.Rows) < 100 || seen >= resp.Total {
			return nil
		}
	}
}
