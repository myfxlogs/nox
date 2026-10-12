// D-181 S2：probe 探测仓评估——scanAndClose 循环中 checkExitCondition 前执行。
// 条件：note 以 "PROBE" 开头 && opened_at + ProbeEvalDays <= now → 评估净结算 → 升级/拒绝。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// probeEvalResult probe 评估结果。
type probeEvalResult struct {
	shouldClose bool   // true → 平仓（拒绝）
	reason      string // 平仓原因
	upgraded    bool   // true → 升级常规仓（清 note）
}

// evaluateProbe D-181 S2：probe 仓到期评估。
// 条件：note 以 "PROBE" 开头 && opened_at + ProbeEvalDays <= now。
// 评估：查 position_swap_snapshots 自 opened_at 起的 swap_usd 跳变（结算事件）。
//   - 净结算 = Σ(两腿跳变 USD) / Σ(两腿 qty) / heldDays → USD/lot/day
//   - 净结算 > 0 → 升级常规仓（清 note）
//   - 净结算 ≤ 0 → 平仓拒绝
//   - 无跳变（broker 不结算）→ 平仓拒绝（实测净零，与 D-182 语义对齐）
//
// 未到期 → 返回 shouldClose=false, upgraded=false（继续走 checkExitCondition）。
// ProbeEnabled=false 或 note 非 PROBE → 跳过（返回 shouldClose=false, upgraded=false）。
// probeDeadline probe 仓到期时刻（D-004 P1#4：单一算式——evaluateProbe 判定与
// positionLoop 到点定时器共用）。不适用（未启用/非 PROBE/无 OpenedAt）→ ok=false。
func (e *Engine) probeDeadline(p *pgstore.Position) (time.Time, bool) {
	if !e.cfg.ProbeEnabled || !strings.HasPrefix(p.Note, "PROBE") || p.OpenedAt == nil {
		return time.Time{}, false
	}
	evalDays := e.cfg.ProbeEvalDays
	if evalDays <= 0 {
		evalDays = 3 // 默认 3 calendar 天
	}
	return p.OpenedAt.Add(time.Duration(evalDays) * 24 * time.Hour), true
}

func (e *Engine) evaluateProbe(ctx context.Context, p *pgstore.Position) probeEvalResult {
	deadline, ok := e.probeDeadline(p)
	if !ok {
		return probeEvalResult{}
	}
	if time.Now().Before(deadline) {
		return probeEvalResult{} // 未到期
	}
	// 查自 opened_at 起的 swap 快照跳变
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		log.Printf("[autotrade] probe pos=%d eval: list legs failed: %v", p.ID, err)
		return probeEvalResult{}
	}
	// 一次拉全部快照（按 venue, ts 排序），按 venue 分组
	allSnaps, err := e.store.ListSwapSnapshotsByPosID(ctx, p.ID)
	if err != nil {
		log.Printf("[autotrade] probe pos=%d eval: list snapshots failed: %v", p.ID, err)
		return probeEvalResult{}
	}
	heldDays := time.Since(*p.OpenedAt).Hours() / 24
	if heldDays < 1 {
		heldDays = 1 // 防除零
	}
	totalSwapUSD := 0.0
	totalQty := 0.0
	hasJump := false
	for _, leg := range legs {
		if leg.Phase != "OPEN" {
			continue
		}
		// 过滤本 leg venue 的快照
		var legSnaps []pgstore.SwapSnapshot
		for _, s := range allSnaps {
			if s.Venue == leg.Venue {
				legSnaps = append(legSnaps, s)
			}
		}
		legSwap, legHasJump := sumSwapJumpsSince(legSnaps, *p.OpenedAt)
		totalSwapUSD += legSwap
		totalQty += leg.EffectiveQty() // D-311 R6：成交口径
		if legHasJump {
			hasJump = true
		}
	}
	if totalQty <= 0 {
		return probeEvalResult{}
	}
	netUsdLotDay := totalSwapUSD / totalQty / heldDays
	if !hasJump || netUsdLotDay == 0 {
		log.Printf("[autotrade] probe pos=%d evaluated: net=0 → rejected (zero settlement in %d days)", p.ID, int(heldDays))
		return probeEvalResult{
			shouldClose: true,
			reason:      fmt.Sprintf("probe rejected: zero settlement (net carry=0 in %d days)", int(heldDays)),
		}
	}
	if netUsdLotDay > 0 {
		log.Printf("[autotrade] probe pos=%d evaluated: net=%.4f → upgraded", p.ID, netUsdLotDay)
		return probeEvalResult{upgraded: true}
	}
	log.Printf("[autotrade] probe pos=%d evaluated: net=%.4f → rejected", p.ID, netUsdLotDay)
	return probeEvalResult{
		shouldClose: true,
		reason:      fmt.Sprintf("probe rejected: settled carry %.4f USD/lot/day", netUsdLotDay),
	}
}

// sumSwapJumpsSince 累计自 since 起的 swap_usd 跳变（相邻快照差值）。
// 返回 (总跳变 USD, 是否有非零跳变)。
// 跳变 = 相邻快照 swap_usd 差值 ≠ 0（结算事件）。
func sumSwapJumpsSince(snapshots []pgstore.SwapSnapshot, since time.Time) (float64, bool) {
	if len(snapshots) < 2 {
		return 0, false
	}
	// snapshots 按 ts 升序（ListSwapSnapshotsByPosID 应保证）
	var total float64
	hasJump := false
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].Ts.Before(since) {
			continue
		}
		diff := snapshots[i].SwapUSD - snapshots[i-1].SwapUSD
		if diff != 0 {
			total += diff
			hasJump = true
		}
	}
	return total, hasJump
}
