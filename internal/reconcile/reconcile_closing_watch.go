// reconcile_closing_watch.go —— CLOSING 看护（自 reconcile_close.go 拆出保 check-lines ≤300；
// 行为零变更，D-385 S1 超限提取位点）。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"strings"
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

// closingNakedLegs D-208②：半平裸奔腿=OPEN FILLED 且无 FILLED/EXTERNAL CLOSE 行。
// （UNKNOWN/FAILED OPEN 腿不计——UNKNOWN 可能已成交另走 uncertainty 径。）
func closingNakedLegs(legs []pgstore.PositionLeg) []pgstore.PositionLeg {
	closed := map[int16]bool{}
	for _, l := range legs {
		if l.Phase == "CLOSE" && (l.Status == "FILLED" || l.Status == "EXTERNAL") {
			closed[l.LegNo] = true
		}
	}
	var out []pgstore.PositionLeg
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" && !closed[l.LegNo] {
			out = append(out, l)
		}
	}
	return out
}

// closingNakedCheck D-208②：CLOSING 半平裸奔升级——非终态腿裸奔时长
// （自 ClosingStartedAt 起；无=异常按全 stuck 计）超 warn 阈→warn（小时桶抑频）、
// 超 crit 阈→critical（每循环）。阈值 app_config 热读（seed-once 0201 落种）；
// 行缺/读败→降级不判（门禁由既有 closingStuckAlert 覆盖，不造新缺省值）。
func (r *Reconciler) closingNakedCheck(ctx context.Context, p *pgstore.Position, now time.Time) {
	warnSec, ok, err := r.store.AppConfigInt64(ctx, "ARB_CLOSING_NAKED_WARN_SEC")
	if err != nil || !ok || warnSec <= 0 {
		return
	}
	critSec, _, _ := r.store.AppConfigInt64(ctx, "ARB_CLOSING_NAKED_CRIT_SEC")
	if critSec <= warnSec {
		critSec = 0 // 未配/非法序→critical 层不生效（warn 层独立可用）
	}
	// 无 ClosingStartedAt=异常态（同 isClosingStuck fail-closed 口径按最大时长计）。
	stuckFor := time.Duration(1<<62 - 1)
	if p.ClosingStartedAt != nil {
		stuckFor = now.Sub(*p.ClosingStartedAt)
	}
	if stuckFor < time.Duration(warnSec)*time.Second {
		return
	}
	legs, err := r.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		log.Printf("[reconcile] closing naked legs pos=%d: %v", p.ID, err)
		return
	}
	naked := closingNakedLegs(legs)
	if len(naked) == 0 {
		return // 无裸腿=非半平形态（卡死径另报）
	}
	var desc []string
	for _, l := range naked {
		desc = append(desc, l.Venue+":"+l.Symbol)
	}
	base := fmt.Sprintf("CLOSING 半平裸奔 position #%d：%d 腿未平（%s），已裸 %s",
		p.ID, len(naked), strings.Join(desc, ","), stuckFor.Round(time.Minute))
	if critSec > 0 && stuckFor >= time.Duration(critSec)*time.Second {
		r.insertAlertCtx(ctx, "critical", base)
		return
	}
	hour := r.now().Unix() / 3600
	r.mu.RLock()
	last, seen := r.nakedAlertHour[p.ID]
	r.mu.RUnlock()
	if seen && last == hour {
		return
	}
	r.mu.Lock()
	r.nakedAlertHour[p.ID] = hour
	r.mu.Unlock()
	r.insertAlertCtx(ctx, "warn", base)
}
