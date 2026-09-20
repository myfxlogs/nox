// cex_venue_key.go —— D-362 S1：撞键组赢家计算（dashboard 侧唯一实现，monitor/
// 持仓查询器/executor 三处同源调用，防键漂移——SSOT §2）。键归一本身在
// reconcile.CexVenueKey（纯函数）。
package dashboard

import (
	"time"

	"arb/internal/store/pgstore"
)

// cexKeyWinners 计算撞键组赢家：同 "venue:type" 多 config 取 created_at 最大
// （=今日后写覆盖赢家语义的显式化，SSOT §0/§6D；created_at 并列取 id 大，确定性）。
// 返回 "venue:type" → 赢家 configID。单 config 组无撞键（CexVenueKey 原样放行）。
func cexKeyWinners(cfgs []pgstore.ExchangeConfig) map[string]int64 {
	winners := make(map[string]int64, len(cfgs))
	bestCreated := make(map[string]time.Time, len(cfgs))
	for _, c := range cfgs {
		k := c.Venue + ":" + c.VenueType
		prev, seen := bestCreated[k]
		if !seen || c.CreatedAt.After(prev) || (c.CreatedAt.Equal(prev) && c.ID > winners[k]) {
			bestCreated[k] = c.CreatedAt
			winners[k] = c.ID
		}
	}
	return winners
}
