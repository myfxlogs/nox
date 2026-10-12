// cex_venue_key.go —— D-362 S1：CEX VenueKey 撞键归一（案 C：现赢家保 canonical）。
// 同一 venue:type 多 exchange_config 时，AccountMonitor/持仓查询器/执行器三处注册表的
// 后写覆盖使 sim（created_at 更晚）恒赢、真盘不可见（SSOT §0 实证 binance:perp）。
// 修法：撞键组内赢家保 "venue:type"，被顶掉者 "venue:type#<configID>"——零既有路由变更，
// 纯新增可见性。本函数为三处同源调用（防键漂移，SSOT §2），数据字段不进后缀。
package reconcile

import (
	"fmt"
	"time"

	"nox/internal/data/keydomain"
)

// CexVenueKey 撞键归一：winners 为 "venue:type" → 赢家 configID（created_at 最大者，
// 由调用方从同一 config 列表计算并三处共用）。非赢家 → "venue:type#<cfgID>"；
// 赢家/无撞键（winners 无该组）→ "venue:type"。# 不进 venue:type 分割路径（SSOT §6C）。
func CexVenueKey(venue, venueType string, cfgID int64, winners map[string]int64) string {
	k := keydomain.Join(venue, venueType)
	if id, collide := winners[k]; collide && id != cfgID {
		return fmt.Sprintf("%s#%d", k, cfgID)
	}
	return k
}

// CexKeyWinners D-369-R2 S1：撞键组赢家计算上提（自 dashboard/cex_venue_key.go 原样
// 移入——monitor/executor/持仓/财务四调用点与 FactCache sim 键集刷新共用同一实现，
// 防双份赢家逻辑漂移）。同 "venue:type" 多 config 取 created_at 最大（并列取 id 大）。
func CexKeyWinners(cfgs []WinnersConfig) map[string]int64 {
	winners := make(map[string]int64, len(cfgs))
	bestCreated := make(map[string]time.Time, len(cfgs))
	for _, c := range cfgs {
		k := keydomain.Join(c.Venue, c.VenueType)
		prev, seen := bestCreated[k]
		if !seen || c.CreatedAt.After(prev) || (c.CreatedAt.Equal(prev) && c.ID > winners[k]) {
			bestCreated[k] = c.CreatedAt
			winners[k] = c.ID
		}
	}
	return winners
}

// WinnersConfig CexKeyWinners 输入行（venue/type/id/created_at 四字段最小面——
// reconcile 不能 import pgstore 既有依赖方向约束的镜像解法）。
type WinnersConfig struct {
	Venue     string
	VenueType string
	ID        int64
	CreatedAt time.Time
}
