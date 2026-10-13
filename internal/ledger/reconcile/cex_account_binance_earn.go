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
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	params.Set("signature", binanceSign(q.apiSecret, params.Encode()))

	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey)
	base := httpx.BinanceApiSegBase(q.baseURL, "sapi")
	body, err := q.get(ctx, base+"/v1/simple-earn/flexible/position?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance earn position: %w", err)
	}
	var resp struct {
		Total int `json:"total"`
		Rows  []struct {
			Asset       string `json:"asset"`
			TotalAmount string `json:"totalAmount"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("binance earn json: %w", err)
	}
	// 逐币种聚合（持仓端点同一资产可多行：活期/阶梯档分列）。
	per := map[string]float64{}
	var ccys []string
	for _, r := range resp.Rows {
		qty, perr := strconv.ParseFloat(r.TotalAmount, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse %s totalAmount=%q: %w", r.Asset, r.TotalAmount, perr)
		}
		if qty <= 0 {
			continue
		}
		if _, ok := per[r.Asset]; !ok {
			ccys = append(ccys, r.Asset)
		}
		per[r.Asset] += qty
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
