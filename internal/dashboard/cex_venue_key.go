// cex_venue_key.go —— D-362 S1：撞键组赢家计算（dashboard 侧唯一实现，monitor/
// 持仓查询器/executor 三处同源调用，防键漂移——SSOT §2）。键归一本身在
// reconcile.CexVenueKey（纯函数）。
package dashboard

import (
	"log"

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

// realOnlyConfigs D-516：simulated=t 行 fail-closed 剔除（warn-once 防轮询刷屏）——
// 必须先于 cexKeyWinners 过滤，保证 sim 行不抢占 canonical 键，也不进入 querier/
// executor/stream 装配（demo 域已收编，绝不拨 demo 端点、也不带 demo 凭据碰实盘域）。
func realOnlyConfigs(cfgs []pgstore.ExchangeConfig, warned *map[int64]bool, tag string) []pgstore.ExchangeConfig {
	if *warned == nil {
		*warned = map[int64]bool{}
	}
	out := make([]pgstore.ExchangeConfig, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Simulated {
			if !(*warned)[c.ID] {
				(*warned)[c.ID] = true
				log.Printf("[%s] cfg#%d %s:%s simulated=t 跳过（demo 域已收编，不连接/不注册/不查询）", tag, c.ID, c.Venue, c.VenueType)
			}
			continue
		}
		out = append(out, c)
	}
	return out
}
