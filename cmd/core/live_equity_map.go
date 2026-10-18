// cmd/core/live_equity_map.go —— liveMap 分币粒度修复（STATE「NAV 日切 liveMap
// 指纹注血」票·待派加急）：map 键=venue:type:currency（与池粒度对齐）。
//
// 病灶：键=venue:type 时 MULTI 户（binance:earn/gate:earn/okx:earn——Equity=Σqty
// 混合单位变更指纹非真值，Currency=="MULTI" 自标，cex_account_*_earn.go 明示
// 「真值在快照分币行」）被当真值覆写至该户每个币种池，fin_nav_daily 非 USD 桶
// 94 币种全腐实证（USDT 桶 7064→108037）。
//
// 修法：Holdings 非空 → 分币键=原生 qty（快照分币行同源同单位，D-285 零换算）；
// 无 Holdings → venue:type:Currency 单键（perp/MT 单币账户）。MULTI 户的
// Equity=Σqty 指纹永不进 map（禁线：指纹禁入判定/账本径）。
package main

import (
	"nox/internal/data/venue"
	v1 "nox/internal/gen/dashboardv1"
)

// liveEquityMapFromAccounts 账户实拉集→venue:type:ccy→权益映射（纯函数可测）。
func liveEquityMapFromAccounts(accs []*v1.AccountInfo) map[string]float64 {
	m := map[string]float64{}
	for _, a := range accs {
		if a == nil {
			continue
		}
		// 池键归一（okx unified/funding 按币种行映射缺实证坑）：spot/perp/futures
		// 原始型与快照池 unified 键对不上→消费点永久映射缺 warn。写侧归一化
		// 同口径（venue.NormalizeVenueType）——USDT 归现金池，非 USDT 各归各币种估值池。
		vt := venue.NormalizeVenueType(a.AssetType, a.Venue, a.VenueType)
		if len(a.Holdings) > 0 {
			for _, h := range a.Holdings {
				if h == nil || h.Ccy == "" {
					continue
				}
				m[a.Venue+":"+vt+":"+h.Ccy] = h.Qty
			}
			continue
		}
		if a.Currency != "" {
			m[a.Venue+":"+vt+":"+a.Currency] = a.Equity
		}
	}
	return m
}
