// auto_close_zombie.go —— D-073/D-108/D-417：OPENING/LEGGED 僵尸收编。
// D-417：LegAtVenue 统一匹配器接入（CEX=Symbol+Side+PosSide，MT=ticket）+
// §1.3b OPENING 推进分支（全腿在场→OPEN/部分→LEGGED）+ venue 未查 fail-closed。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// zombieSweep OPENING+LEGGED 僵尸收编驱动（Run 3b/3c 公共段，D-417 抽出消 reconcile_close 超线）。
// 返回本轮处置失败计数（归档 err → critical 已在内部插入）。
func (r *Reconciler) zombieSweep(
	ctx context.Context,
	openingPositions, leggedPositions []pgstore.Position,
	ordersByVenue map[string][]exec.OpenedOrder,
	now time.Time,
) int {
	failed := 0
	for _, p := range openingPositions {
		legsCtx, legsCancel := r.withTimeout(ctx)
		legs, err := r.store.ListPositionLegs(legsCtx, p.ID)
		legsCancel()
		if err != nil {
			log.Printf("[reconcile] list legs for OPENING zombie pos=%d failed: %v (skip this round)", p.ID, err)
			continue
		}
		if !isOPENINGZombie(&p, legs, now) {
			continue
		}
		if err := r.autoFailOPENINGZombie(ctx, &p, ordersByVenue); err != nil {
			failed++
			msg := fmt.Sprintf("OPENING 僵尸归档失败 position #%d: %v", p.ID, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
		}
	}
	for _, p := range leggedPositions {
		if !isLeggedZombie(&p, now) {
			continue
		}
		if err := r.autoFailLeggedZombie(ctx, &p, ordersByVenue); err != nil {
			failed++
			msg := fmt.Sprintf("LEGGED 僵尸归档失败 position #%d: %v", p.ID, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
		}
	}
	return failed
}

// autoFailOPENINGZombie OPENING 僵尸收编（超 24h）：venue 证实在场腿数 → 推进/归档。
// D-417 §1.3b：匹配器落地后该分支有可信事实源，改状态推进——
// 全部 OPEN 腿在场→OPEN（恢复评估/平仓链）；部分在场→LEGGED（进 D-414 收编域）；
// 全无→FAILED+warn。venue 未成功查询（ordersByVenue 无键）→ fail-closed 本轮不动。
func (r *Reconciler) autoFailOPENINGZombie(
	ctx context.Context,
	pos *pgstore.Position,
	ordersByVenue map[string][]exec.OpenedOrder,
) error {
	posID := pos.ID
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}
	// 统计 phase=OPEN 有凭据腿在场情况（D-417：LegAtVenue 统一匹配，CEX 走 Symbol+Side）。
	open, present := 0, 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.OrderID == "" {
			continue
		}
		orders, ok := ordersByVenue[leg.Venue]
		if !ok {
			log.Printf("[reconcile] OPENING 僵尸 pos=%d 腿 venue=%s 本轮未成功查询，fail-closed 不推进", posID, leg.Venue)
			return nil // venue 查询出错 → fail-closed（§1.3b）
		}
		open++
		if exec.LegAtVenue(leg, orders) {
			present++
		}
	}
	target := ""
	switch {
	case open == 0, present == 0:
		target = "FAILED" // 无有效凭据腿 / venue 全无 → 归档（旧路径语义）
	case present == open:
		target = "OPEN" // §1.3b：全腿在场=开仓已完成，恢复评估/平仓链
	default:
		target = "LEGGED" // §1.3b：部分在场=半成交，进 D-414 收编域
	}
	if err := r.updateStatusRetry(ctx, posID, target); err != nil {
		return err
	}
	var msg string
	if target == "FAILED" {
		msg = fmt.Sprintf("OPENING 僵尸自动归档 position #%d（超 24h 未完成开仓，转 FAILED）", posID)
	} else {
		msg = fmt.Sprintf("OPENING 僵尸 position #%d venue 证实 %d/%d 腿在场 → %s", posID, present, open, target)
	}
	r.insertAlertCtx(ctx, "warn", msg)
	log.Printf("[reconcile] %s", msg)
	return nil
}

// updateStatusRetry D-417 §1.3b：UpdatePositionStatus 失败重试 ≤3 次后升 critical（不再仅 log）。
func (r *Reconciler) updateStatusRetry(ctx context.Context, posID int64, status string) error {
	var err error
	for i := 0; i < 3; i++ {
		sub, cancel := r.withTimeout(ctx)
		err = r.store.UpdatePositionStatus(sub, posID, status)
		cancel()
		if err == nil || errors.Is(err, pgstore.ErrTerminalState) {
			return nil
		}
	}
	r.insertAlertCtx(ctx, "critical",
		fmt.Sprintf("position #%d 状态推进 %s 失败（重试 3 次）: %v", posID, status, err))
	return fmt.Errorf("update status %s: %w", status, err)
}

// isOPENINGZombie 判断 position 是否为 OPENING 僵尸。
// 判定条件（满足任一即僵尸）：
//  1. 超 24h（opened_at 存在且 now.Sub > 24h）——原条件；
//  2. 无 opened_at 且无任何 legs——补充条件（#254 这种执行中断连时间戳都没写的仓，
//     原条件永远跳过；无时间戳+无腿=执行中断无进展，判僵尸归档）。
//     防新建仓窗口误杀：有腿=执行中不算（腿已写入=引擎已推进）。
//
// legs 为该 position 的全部腿（调用方查 store.ListPositionLegs 传入）。
func isOPENINGZombie(pos *pgstore.Position, legs []pgstore.PositionLeg, now time.Time) bool {
	if pos.Status != "OPENING" {
		return false
	}
	// 条件1：超 24h
	if pos.OpenedAt != nil && now.Sub(*pos.OpenedAt) > 24*time.Hour {
		return true
	}
	// 条件2：无 opened_at 且无任何 legs（执行中断无进展；有腿=执行中不判）
	if pos.OpenedAt == nil && len(legs) == 0 {
		return true
	}
	return false
}

// autoFailLeggedZombie LEGGED 僵尸收编（超 24h）：FILLED 腿在 venue 有 → warn 不归档（交人工/下轮）；
// 无 FILLED 腿在 venue → FAILED 归档 + warn（D-108 P2 T3，模式同 autoFailOPENINGZombie）。
// LEGGED = 部分成交持仓（一腿 FILLED 一腿 FAILED），超 24h 未恢复 → 需处置。
func (r *Reconciler) autoFailLeggedZombie(
	ctx context.Context,
	pos *pgstore.Position,
	ordersByVenue map[string][]exec.OpenedOrder,
) error {
	posID := pos.ID
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}
	// 查是否有 FILLED 腿在 venue 存在（LEGGED 至少一腿 FILLED）
	for _, leg := range legs {
		if leg.OrderID == "" || leg.Status != "FILLED" {
			continue
		}
		// D-417：venue 未成功查询 → fail-closed 不误杀；LegAtVenue 统一匹配（CEX=Symbol+Side）。
		orders, ok := ordersByVenue[leg.Venue]
		if !ok {
			log.Printf("[reconcile] LEGGED 僵尸 pos=%d 腿 venue=%s 本轮未成功查询，fail-closed 不归档", posID, leg.Venue)
			return nil
		}
		if exec.LegAtVenue(leg, orders) {
			// D-108 P2 T3：FILLED 腿在 venue 有 → 不置 FAILED（venue 真有持仓，误平风险），
			// 记录告警交人工/下轮对账处置（P1 A3 同语义）。
			msg := fmt.Sprintf("LEGGED 僵尸 position #%d 腿 ticket=%s 在 venue 有持仓，不归档 FAILED（交人工/对账）", posID, leg.OrderID)
			r.insertAlertCtx(ctx, "warn", msg)
			log.Printf("[reconcile] %s", msg)
			return nil
		}
	}
	// LEGGED 僵尸无 FILLED 腿在 venue → FAILED 归档
	if err := r.updateStatusRetry(ctx, posID, "FAILED"); err != nil {
		return err
	}
	msg := fmt.Sprintf("LEGGED 僵尸自动归档 position #%d（超 24h 未恢复，转 FAILED）", posID)
	r.insertAlertCtx(ctx, "warn", msg)
	log.Printf("[reconcile] %s", msg)
	return nil
}

// isLeggedZombie 判断 position 是否为 LEGGED 僵尸（超 24h 未恢复）。
// LEGGED = 部分成交持仓，超 24h 仍 LEGGED = 僵尸需处置。
func isLeggedZombie(pos *pgstore.Position, now time.Time) bool {
	if pos.Status != "LEGGED" {
		return false
	}
	if pos.OpenedAt != nil && now.Sub(*pos.OpenedAt) > 24*time.Hour {
		return true
	}
	return false
}
