// carry_universe_alert.go D-336 S3：阈值告警——任一 canonical 净 carry ≥ 8%（D-191 资本
// 任务书门槛）且 swap_free → InsertAlert「真候选浮现」（feishu 投递+去重由既有 pusher 链）。
// 4-8% 只进排名不推送（观察线，BTC 当前 6.65% 落此档——在视野里，不喊人）。
package scan

import (
	"context"
	"fmt"
	"log"

	"arb/internal/store/pgstore"
)

// AlertThresholdPct D-336 S3：真候选推送线。
const AlertThresholdPct = 8.0

// AlertSink D-336 S3：告警落点（pgstore.InsertAlert；ruleID=nil 系统级）。
type AlertSink func(ctx context.Context, level, message string) error

// EmitThresholdAlerts 阈值告警（net≥8% 且 swap_free；每轮每 canonical 至多一条，
// 去重由 D-301c 同指纹冷却链负责）。
func EmitThresholdAlerts(ctx context.Context, sink AlertSink, rows []UniverseRow, insert func(ctx context.Context, ruleID *int64, level, message string) error) {
	for _, r := range rows {
		if r.Structure == "spot_carry" {
			continue // D-350R1 S3：spot 行不触发「真候选浮现」（1369 行/轮告警洪水；开仓事件告警在 wire_scan）
		}
		if r.NetAnnual >= AlertThresholdPct && r.SwapFree {
			msg := fmt.Sprintf("[carry 宇宙] 真候选浮现: %s 净 carry %.2f%%/yr (%s, %s×%s, swap-free)——发现≠授权，白名单/开仓需业主拍板",
				r.Canonical, r.NetAnnual, r.Structure, r.MtVenue, r.CexVenue)
			log.Printf("[scan] %s", msg)
			if insert != nil {
				if err := insert(ctx, nil, "info", msg); err != nil {
					log.Printf("[scan] alert insert %s: %v", r.Canonical, err)
				}
			}
		}
	}
}

var _ = pgstore.CarryUniverseRow{}
