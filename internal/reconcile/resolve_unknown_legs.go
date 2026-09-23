// resolve_unknown_legs.go —— D-421 §1.5/§1.6：UNKNOWN 自动消解 + LEGGED 收编前置守卫。
//
// 消解器（30s tick 挂 StartFastRetryLoop）：腿 status='UNKNOWN' AND order_id != ”
// → CEX 按 OrderStatusQuerier 订单级查单（ordId 优先 clOrdId 兜底），MT 复用
// OrderHistoryQuerier 按 ticket 查（D-416 在册面）。filled/partial→FILLED 回填 +
// 仓态重评（全腿齐→OPEN；无 UNKNOWN 且有 FAILED→LEGGED 进 D-414）；canceled+零成交
// →FAILED；≥10 轮不可判→critical+留 UNKNOWN 人工。
//
// 收编前置守卫（§1.6）：fastRetryLegged ClaimClose 前，FAILED+OrderID 腿先查单——
// 实际已成交=中止收编回填 FILLED（假未成交收编=换腿裸奔，D-414 互咬补丁）；
// 确定未成交=继续收编；查询失败=仓标 uncertainty+critical 本轮不平（fail-closed）。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// unknownMaxRounds 消解累计上限（≥10 轮不可判→critical 留人工，SSOT §1.5）。
const unknownMaxRounds = 10

// SetOrderStatusQueriers 注册 CEX 订单级查单器集合（venueKey→querier，镜像 SetCEXQueriers）。
func (r *Reconciler) SetOrderStatusQueriers(qs map[string]exec.OrderStatusQuerier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orderStatusQs = qs
}

func (r *Reconciler) orderStatusQuerier(venueKey string) exec.OrderStatusQuerier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.orderStatusQs[venueKey]
}

// resolveUnknownLegs 消解器单轮（30s tick）。
func (r *Reconciler) resolveUnknownLegs(ctx context.Context) {
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListUnknownLegs(sub, 50)
	cancel()
	if err != nil {
		log.Printf("[reconcile] resolve unknown legs list: %v", err)
		return
	}
	for i := range legs {
		r.resolveOneUnknownLeg(ctx, &legs[i])
	}
}

// resolveOneUnknownLeg 单腿消解：查单→映射→仓态重评。任何步失败=留 UNKNOWN 下轮重试（幂等）。
func (r *Reconciler) resolveOneUnknownLeg(ctx context.Context, leg *pgstore.PositionLeg) {
	if isMTVenueKeyR(leg.Venue) {
		r.resolveMTUnknownLeg(ctx, leg)
		return
	}
	q := r.orderStatusQuerier(leg.Venue)
	if q == nil {
		r.unknownStuck(ctx, leg, "no order status querier for "+leg.Venue)
		return
	}
	sub, cancel := r.withTimeout(ctx)
	state, qty, px, fee, err := q.QueryOrderStatus(sub, leg.OrderID, leg.ClientOrderID, leg.Symbol)
	cancel()
	switch {
	case err != nil:
		log.Printf("[reconcile] resolve unknown leg=%d order=%s query: %v", leg.ID, leg.OrderID, err)
		r.unknownStuck(ctx, leg, err.Error())
	case state == "filled" || state == "partial":
		if qty <= 0 {
			r.unknownStuck(ctx, leg, "filled with zero qty")
			return
		}
		r.applyLegResolved(ctx, leg, px, qty, fee)
	case state == "canceled" && qty <= 0:
		sub, cancel = r.withTimeout(ctx)
		err = r.store.ResolveLegFail(sub, leg.ID)
		cancel()
		if err != nil {
			log.Printf("[reconcile] resolve unknown leg=%d fail write: %v", leg.ID, err)
			return
		}
		log.Printf("[reconcile] resolve unknown leg=%d order=%s canceled zero-fill → FAILED", leg.ID, leg.OrderID)
		r.reevalAfterResolve(ctx, leg.PositionID)
	default:
		r.unknownStuck(ctx, leg, "state="+state)
	}
}

// resolveMTUnknownLeg MT 腿消解（D-427 I1 两证据序）：
// 主证据=持仓在场（OpenedByVenueKey 按 ticket 命中 → OpenPrice/Volume 权威字段回填）；
// 副证据=历史已开已平（OrderHistoryQuerier，OpenPrice 优先 ClosePrice 兜底）；
// 双 miss/查询 err → unknownStuck（fail-closed 不动）。
func (r *Reconciler) resolveMTUnknownLeg(ctx context.Context, leg *pgstore.PositionLeg) {
	sub, cancel := r.withTimeout(ctx)
	orders, err := r.OpenedByVenueKey(sub, leg.Venue)
	cancel()
	if err == nil {
		for _, o := range orders {
			if o.Ticket == leg.OrderID {
				fee := o.Commission
				if fee < 0 {
					fee = -fee
				}
				r.applyLegResolved(ctx, leg, o.OpenPrice, o.Volume, fee)
				return
			}
		}
	}
	q := r.findHistoryQuerier(leg.Venue)
	if q == nil {
		r.unknownStuck(ctx, leg, "no history querier for "+leg.Venue)
		return
	}
	sub, cancel = r.withTimeout(ctx)
	h, err := q.QueryOrderHistory(sub, leg.OrderID, 3)
	cancel()
	px := float64(0)
	if h != nil {
		px = h.OpenPrice
		if px <= 0 {
			px = h.ClosePrice // 开仓价缺场 → 收盘价如实兜底
		}
	}
	if err != nil || h == nil || px <= 0 {
		r.unknownStuck(ctx, leg, "history no hit")
		return
	}
	fee := h.Commission
	if fee < 0 {
		fee = -fee
	}
	r.applyLegResolved(ctx, leg, px, leg.EffectiveQty(), fee)
}

// applyLegResolved 腿 FILLED 回填 + 仓态重评（CEX/MT 消解公共尾）。
func (r *Reconciler) applyLegResolved(ctx context.Context, leg *pgstore.PositionLeg, px, qty, fee float64) {
	sub, cancel := r.withTimeout(ctx)
	err := r.store.ResolveLegFill(sub, leg.ID, px, qty, fee)
	cancel()
	if err != nil {
		log.Printf("[reconcile] resolve unknown leg=%d fill write: %v", leg.ID, err)
		return
	}
	log.Printf("[reconcile] resolve unknown leg=%d order=%s → FILLED price=%v qty=%v", leg.ID, leg.OrderID, px, qty)
	r.reevalAfterResolve(ctx, leg.PositionID)
}

// reevalAfterResolve 消解后仓态重评：全腿 FILLED→OPEN+清 uncertainty；
// 无 UNKNOWN 且有 FAILED→LEGGED（确定未成交，进 D-414 收编域）+清 uncertainty；
// 仍有 UNKNOWN→不动（CLOSING+uncertainty 待后续轮次）。
func (r *Reconciler) reevalAfterResolve(ctx context.Context, posID int64) {
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		log.Printf("[reconcile] resolve reeval legs pos=%d: %v", posID, err)
		return
	}
	filled, unknown, failed := 0, 0, 0
	for _, l := range legs {
		if l.Phase != "OPEN" {
			continue
		}
		switch l.Status {
		case "FILLED":
			filled++
		case "UNKNOWN":
			unknown++
		case "FAILED":
			failed++
		}
	}
	if unknown > 0 {
		return
	}
	var status string
	if failed == 0 && filled > 0 {
		status = "OPEN"
	} else if failed > 0 {
		status = "LEGGED"
	} else {
		return
	}
	sub, cancel = r.withTimeout(ctx)
	_ = r.store.UpdatePositionStatus(sub, posID, status)
	_ = r.store.SetPositionUncertainty(sub, posID, "")
	cancel()
	delete(r.unknownRounds, posID)
	log.Printf("[reconcile] resolve pos=%d → %s", posID, status)
}

// unknownStuck 不可判计数+上限告警（posID 粒度：一仓多腿同因不重复放大）。
func (r *Reconciler) unknownStuck(ctx context.Context, leg *pgstore.PositionLeg, why string) {
	r.unknownRounds[leg.PositionID]++
	if r.unknownRounds[leg.PositionID] == unknownMaxRounds {
		r.insertAlertCtx(ctx, "critical",
			fmt.Sprintf("resolve unknown leg stuck pos=%d leg=%d venue=%s order=%s (%s) — manual verify required",
				leg.PositionID, leg.ID, leg.Venue, leg.OrderID, why))
	}
}

// preflightLeggedFailedLegs §1.6 收编前置守卫：LEGGED 收编前查 FAILED+OrderID 腿真实终态。
// 返回 false=本轮中止收编（已成交回填/查询失败 fail-closed）；true=可继续既有收编。
func (r *Reconciler) preflightLeggedFailedLegs(ctx context.Context, p *pgstore.Position) bool {
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, p.ID)
	cancel()
	if err != nil {
		log.Printf("[reconcile] legged preflight legs pos=%d: %v", p.ID, err)
		return true // 腿查询失败沿用旧路径（收编内 checkResidualLegs 自带拦）
	}
	for i := range legs {
		l := &legs[i]
		if l.Phase != "OPEN" || l.Status != "FAILED" || l.OrderID == "" {
			continue
		}
		q := r.orderStatusQuerier(l.Venue)
		if q == nil {
			continue // 无查单面（MT venue 走历史查证链）→ 旧路径
		}
		sub, cancel = r.withTimeout(ctx)
		state, qty, px, fee, qerr := q.QueryOrderStatus(sub, l.OrderID, l.ClientOrderID, l.Symbol)
		cancel()
		switch {
		case qerr != nil, state != "filled" && state != "partial" && state != "canceled":
			// 查询失败/状态模糊 → 标 uncertainty+critical 本轮不平（fail-closed）。
			reason := "legged preflight order query inconclusive: venue=" + l.Venue + " order=" + l.OrderID
			sub, cancel = r.withTimeout(ctx)
			_ = r.store.SetPositionUncertainty(sub, p.ID, reason)
			cancel()
			r.insertAlertCtx(ctx, "critical",
				fmt.Sprintf("legged preflight inconclusive pos=%d %s", p.ID, reason))
			if qerr != nil {
				log.Printf("[reconcile] legged preflight pos=%d order=%s query: %v", p.ID, l.OrderID, qerr)
			}
			return false
		case (state == "filled" || state == "partial" || state == "canceled") && qty > 0:
			// 实际已成交（含 canceled+部分成交：撤单前有量=真实持仓）→ 回填 FILLED +
			// 仓态重评（全腿齐→OPEN），中止收编。
			sub, cancel = r.withTimeout(ctx)
			_ = r.store.ResolveLegFill(sub, l.ID, px, qty, fee)
			cancel()
			log.Printf("[reconcile] legged preflight pos=%d leg=%d order=%s actually filled price=%v qty=%v → abort collect",
				p.ID, l.ID, l.OrderID, px, qty)
			r.reevalAfterResolve(ctx, p.ID)
			return false
		}
		// canceled+零成交 / 其余终态零量 → 确定未成交，继续收编。
	}
	return true
}

// isMTVenueKeyR venueKey 尾缀判 MT（exec.isMTVenueKey 未导出，本包局部判据同形）。
func isMTVenueKeyR(v string) bool {
	return strings.HasSuffix(v, ":mt5") || strings.HasSuffix(v, ":mt4")
}
