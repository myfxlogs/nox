// reconcile_closing_watch.go —— CLOSING 看护（自 reconcile_close.go 拆出保 check-lines ≤300；
// 行为零变更，D-385 S1 超限提取位点）。
package reconcile

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// closingStuckTimeout D-159 W2a R3-6：CLOSING 超 30min 未完成=卡住（平仓 I/O 失败或残量未归零）。
const closingStuckTimeout = 30 * time.Minute

// isClosingStuck D-159 W2a R3-6 / D-189 P0-4：判断 CLOSING 持仓是否卡住（超时未完成）。
// D-189 P0-4：改用 ClosingStartedAt（进入 CLOSING 的真实起始时间），不再用 opened_at（开仓时间）。
// 无 ClosingStartedAt = 异常（直接判卡住，fail-closed）；迁移已为存量 CLOSING 行补当前时间。

// closingStuckAlert D-245：CLOSING 看护告警分层——卡死 <2h 维持 critical 每循环；
// ≥2h 降为 warn 且每小时至多 1 条（closingAlertHour 小时桶，进程重启最多重复 1 条）。
// ResumeClose 重试节奏不受影响（降的是告警不是重试）。
func (r *Reconciler) closingStuckAlert(ctx context.Context, p *pgstore.Position, stuckFor time.Duration, msg string) {
	if p.ClosingStartedAt == nil || stuckFor < closingTierWarnAfter {
		r.insertAlertCtx(ctx, "critical", msg)
		return
	}
	hour := r.now().Unix() / 3600
	r.mu.RLock()
	last, ok := r.closingAlertHour[p.ID]
	r.mu.RUnlock()
	if ok && last == hour {
		return
	}
	r.mu.Lock()
	r.closingAlertHour[p.ID] = hour
	r.mu.Unlock()
	r.insertAlertCtx(ctx, "warn", msg)
}

// closingTierWarnAfter D-245：warn 分层门槛（卡死 ≥2h 降频）。
const closingTierWarnAfter = 2 * time.Hour

func isClosingStuck(pos *pgstore.Position, now time.Time) bool {
	if pos.Status != "CLOSING" {
		return false
	}
	if pos.ClosingStartedAt == nil {
		return true // 无 closing_started_at=异常（fail-closed）
	}
	return now.Sub(*pos.ClosingStartedAt) > closingStuckTimeout
}
