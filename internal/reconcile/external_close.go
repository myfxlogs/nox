// external_close.go —— D-015 ②：venue 平仓/强平/ADL 推送事件入对账。
// 系统未发单的平仓（手动平/venue 侧强平/ADL）只留「券商侧已无此腿」告警噪音；
// 事件到达即落 CLOSE EXTERNAL 标记腿（价格/费用从帧取，帧无则 NULL 不写假值），
// 残腿判定（residualLegVenues）与平仓看护（ResumeClose 残量校验）同享该行。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"

	"arb/internal/store/pgstore"
)

// DegradedVenues 当前处于平仓通道降级态的 venue 集合（fastRetry 升级置位、
// ResumeClose 成功清零）。Gate.Submit 闸据此拒新开（fail-closed 在闸侧）。
func (r *Reconciler) DegradedVenues() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.degradedVenues))
	for v := range r.degradedVenues {
		out = append(out, v)
	}
	return out
}

// MarkVenueLegClosed venue 平仓事件（venueKey+symbol+side 匹配）→ 系统持仓 OPEN FILLED
// 腿落 CLOSE status=EXTERNAL 标记行 + Kick 叫醒对账。幂等：同 leg_no 已有 CLOSE
// FILLED/EXTERNAL 或并发重复帧 → 不重复写。返 posID/found=是否匹配到系统腿
// （外单平仓不入系统腿表，Kick 仍发——对账面用 venue 实持核对外单差异）。
func (r *Reconciler) MarkVenueLegClosed(ctx context.Context, venueKey, symbol, side string, px *float64, feeUSD *float64) (posID int64, found bool, err error) {
	defer r.Kick()
	sub, cancel := r.withTimeout(ctx)
	defer cancel()
	var openLeg pgstore.PositionLeg
	var legs []pgstore.PositionLeg
	for _, status := range []string{"OPEN", "CLOSING", "LEGGED"} {
		positions, lerr := r.store.ListPositions(sub, status, 200, 0)
		if lerr != nil {
			return 0, false, fmt.Errorf("list %s positions: %w", status, lerr)
		}
		for i := range positions {
			ls, lerr := r.store.ListPositionLegs(sub, positions[i].ID)
			if lerr != nil {
				return 0, false, fmt.Errorf("list legs pos=%d: %w", positions[i].ID, lerr)
			}
			for _, l := range ls {
				if l.Phase != "OPEN" || l.Status != "FILLED" || l.Venue != venueKey || l.Symbol != symbol {
					continue
				}
				if side != "" && l.Side != side {
					continue
				}
				posID, openLeg, legs, found = positions[i].ID, l, ls, true
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return 0, false, nil
	}
	// 同 leg_no CLOSE 终态已存在 → 幂等跳过；非终态行（FAILED/UNKNOWN 等）存在时
	// 仍补写——venue 帧是「已平」事实，优先于本地尝试记录。
	maxAttempt := int16(0)
	done := false
	for _, l := range legs {
		if l.LegNo != openLeg.LegNo || l.Phase != "CLOSE" {
			continue
		}
		if l.AttemptNo > maxAttempt {
			maxAttempt = l.AttemptNo
		}
		if l.Status == "FILLED" || l.Status == "EXTERNAL" {
			done = true
		}
	}
	if done {
		return posID, true, nil
	}
	closeSide := "SELL"
	if openLeg.Side == "SELL" {
		closeSide = "BUY"
	}
	qty := openLeg.EffectiveQty()
	leg := &pgstore.PositionLeg{
		PositionID: posID, LegNo: openLeg.LegNo, Phase: "CLOSE", AttemptNo: maxAttempt + 1,
		Venue: venueKey, Symbol: openLeg.Symbol, Side: closeSide,
		Qty: qty, PriceExpected: 0, PriceActual: px, OrderID: openLeg.OrderID,
		Status: "EXTERNAL", FilledQty: &qty, AvgFillPrice: px, FeeUSD: feeUSD,
	}
	if px != nil {
		leg.PriceExpected = *px
	}
	if err := r.store.CreatePositionLeg(sub, leg); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return posID, true, fmt.Errorf("mark external close pos=%d leg=%d: %w", posID, openLeg.LegNo, err)
	}
	log.Printf("[reconcile] external close marked pos=%d leg=%d %s %s", posID, openLeg.LegNo, venueKey, symbol)
	return posID, true, nil
}
