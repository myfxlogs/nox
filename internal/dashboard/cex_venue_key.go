// cex_venue_key.go —— D-362 S1：撞键组赢家计算（dashboard 侧唯一实现，monitor/
// 持仓查询器/executor 三处同源调用，防键漂移——SSOT §2）。键归一本身在
// reconcile.CexVenueKey（纯函数）。
package dashboard

import (
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// cexKeyWinners D-369-R2 S1：薄转发 reconcile.CexKeyWinners（赢家逻辑单一实现，
// 防双份漂移；本文件仅保留 pgstore.ExchangeConfig → WinnersConfig 适配）。
func cexKeyWinners(cfgs []pgstore.ExchangeConfig) map[string]int64 {
	in := make([]reconcile.WinnersConfig, len(cfgs))
	for i, c := range cfgs {
		in[i] = reconcile.WinnersConfig{Venue: c.Venue, VenueType: c.VenueType, ID: c.ID, CreatedAt: c.CreatedAt}
	}
	return reconcile.CexKeyWinners(in)
}
