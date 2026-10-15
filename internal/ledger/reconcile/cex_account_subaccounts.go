// cex_account_subaccounts.go —— D-363 S2：CEX 资金（funding）/理财（earn）子账户分派。
// 只报 USDT 余额（D-364 v2 同语义：非 USDT 标不读取不统计，DOGE/USDC 等全跳）。
// 路由核证点④（9-20 真 key 实探留证）：binance sapi 走代理根路径（/api 前缀 404 实探）。
// venue 实现按所拆件：cex_account_okx_subaccounts.go / cex_account_gate_subaccounts.go
// （binance earn 在 cex_account_binance_earn.go）——check-lines 拆分零语义变化。
package reconcile

import (
	"context"
	"fmt"
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
