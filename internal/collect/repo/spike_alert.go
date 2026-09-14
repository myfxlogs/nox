// spike_alert.go GC007 利率飙升告警判定+去重。
// 从 collector.go 拆出（D-275 纯重构）。
package repo

import (
	"context"
	"fmt"
	"log"
)

// checkSpike 判定 GC007 spike 告警（warn>spikeWarn / critical>spikeCrit）。
// 去重：同品种同日同阈值只报 1 次（AlreadyAlertedToday 查 alerts 表，pattern 含品种代码）。
func (c *RepoCollector) checkSpike(ctx context.Context, rates []RepoRateRow) {
	if c.alertFn == nil {
		return
	}
	for _, r := range rates {
		if r.Code != "204007" { // 只对 GC007 告警（设计 §2.3.2）
			continue
		}
		level, pattern, msg := "", "", ""
		if r.RatePct > c.spikeCrit {
			level = "critical"
			pattern = "[repo] GC007%critical%"
			msg = fmt.Sprintf("[repo] GC007 年化 %.2f%%，利率飙升窗口（超 critical 阈值 %.1f%%）", r.RatePct, c.spikeCrit)
		} else if r.RatePct > c.spikeWarn {
			level = "warn"
			pattern = "[repo] GC007%建议逆回购%" // R3：warn 独有"建议逆回购"串，critical 用"利率飙升窗口"，互不交叉
			msg = fmt.Sprintf("[repo] GC007 年化 %.2f%%，超过阈值 %.1f%%，建议逆回购", r.RatePct, c.spikeWarn)
		}
		if level == "" {
			continue
		}
		// 去重：同品种同日同阈值只报 1 次
		alerted, err := c.store.AlreadyAlertedToday(ctx, pattern)
		if err != nil {
			log.Printf("repo: dedup check: %v", err)
			continue
		}
		if alerted {
			continue
		}
		if err := c.alertFn(level, msg); err != nil {
			log.Printf("repo: alert %s: %v", level, err)
		}
	}
}
