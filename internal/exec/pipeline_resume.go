// pipeline_resume.go —— D-189 P0-4：ResumeClose 公共执行核心 + per-position close 锁。
// 从 pipeline_legs_close.go 拆出（pipeline.go >300 行软警告）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// ResumeClose D-189 P0-4：重触发 CLOSING 持仓的平仓流程（reconcile 超时自动重试入口）。
// 幂等：CLOSED/FAILED 直接返回 nil；只有 CLOSING 进入核心；其他状态返回 error。
// 不调 ClaimClose（position 已是 CLOSING）；走与 ClosePosition 相同的 leg 处理 + 残量校验 + PnL 汇总 + CLOSED。
// per-position close 锁：同进程并发只允许一个执行（ClosePosition/ResumeClose 共用）。
func (p *Pipeline) ResumeClose(ctx context.Context, posID int64) error {
	if p.store == nil {
		return fmt.Errorf("pipeline store not wired")
	}
	// D-189 P0-4：per-position close 锁
	unlock := p.lockClose(posID)
	defer unlock()

	pos, err := p.store.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("get position %d: %w", posID, err)
	}
	if pos == nil {
		return fmt.Errorf("position %d not found", posID)
	}
	switch pos.Status {
	case "CLOSED", "FAILED":
		return nil // 幂等
	case "CLOSING":
		// 进入核心
	default:
		return fmt.Errorf("position %d status=%s not CLOSING (ResumeClose only for CLOSING)", posID, pos.Status)
	}
	return p.closePositionCore(ctx, posID)
}

// lockClose D-189 P0-4：per-position close 锁。返回 unlock 函数。
// closeMu 保护 closeSem map；per-posID mutex 保证同 position 串行，不同 position 并行。
// 零值 Pipeline（closing/closeSem nil）→ no-op（测试安全）。
func (p *Pipeline) lockClose(posID int64) func() {
	if p.closeSem == nil {
		return func() {} // 零值 Pipeline 安全
	}
	p.closeMu.Lock()
	mu, ok := p.closeSem[posID]
	if !ok {
		mu = &sync.Mutex{}
		p.closeSem[posID] = mu
	}
	p.closeMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// closePositionCore D-189 P0-4：共享平仓核心（ClosePosition 与 ResumeClose 共用）。
// 职责：leg 处理（发平仓单/历史回填）→ 残量校验 → PnL 校准 → 现金流完整性 → CLOSED。
// legNo 去重：closeByLegNo map（废弃 closedTickets[OrderID]）。
// broker preflight：对无 CLOSE leg 的 OPEN 腿查 verifier；broker 已无腿则历史回填，无历史则 fail-closed。
// 现金流完整性：已有 CLOSE leg 的腿检查 cashflows 缺失，从权威历史补回。
func (p *Pipeline) closePositionCore(ctx context.Context, posID int64) error {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}

	// D-189 P0-4 S4-1：closeByLegNo 去重（废弃 closedTickets[OrderID]）。
	closeByLegNo := make(map[int16]pgstore.PositionLeg)
	for _, l := range legs {
		if l.Phase == "CLOSE" && l.Status == "FILLED" {
			closeByLegNo[l.LegNo] = l
		}
	}

	// 计价币→USD 换算（与 ClosePosition 同口径）
	pos, err := p.store.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("get position %d: %w", posID, err)
	}
	quoteCcy := notional.QuoteCcy(canonicalFromOppID(pos.OpportunityID))
	fx := 1.0
	fxMissing := false
	if quoteCcy != "" && quoteCcy != "USD" {
		if p.fxResolver != nil {
			rate, ok := p.fxResolver.USDPer(ctx, quoteCcy, time.Now())
			if !ok || rate <= 0 {
				fxMissing = true
				fx = 0
			} else {
				fx = rate
			}
		} else {
			fxMissing = true
			fx = 0
		}
	}

	var totalPnL float64
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		// D-189 P0-4 S4-1：legNo 去重——已有 CLOSE leg 跳过发单。
		// D-195-C2 §6.4：部分成交残量继续——CLOSE leg filled_qty > 0 且 < OPEN leg qty 时只对剩余量发单。
		if closeLeg, hasClose := closeByLegNo[leg.LegNo]; hasClose {
			closeFilledQty := 0.0
			hasPartialFill := closeLeg.FilledQty != nil && *closeLeg.FilledQty > 0
			if hasPartialFill {
				closeFilledQty = *closeLeg.FilledQty
			}
			if !hasPartialFill || closeFilledQty >= leg.Qty {
				// 无部分成交（nil/0=未成交或全量）或已全部平仓 → cashflows 补回后跳过。
				if err := p.backfillMissingCashflows(ctx, posID, leg, fx); err != nil {
					return fmt.Errorf("pos=%d leg=%d backfill cashflow: %w", posID, leg.LegNo, err)
				}
				continue
			}
			// 部分成交（0 < filled_qty < open_qty）→ 继续对剩余量发单（不跳过本腿）
			log.Printf("[exec] resume close leg %d partial fill: openQty=%.4f closeFilledQty=%.4f remaining=%.4f",
				leg.ID, leg.Qty, closeFilledQty, leg.Qty-closeFilledQty)
		}
		// D-189 P0-4 S4-2：broker preflight——查 verifier 确认腿是否仍在仓。
		stillOpen, fill, err := p.brokerPreflight(ctx, posID, leg)
		if err != nil {
			return fmt.Errorf("pos=%d leg=%d broker preflight: %w", posID, leg.LegNo, err)
		}
		if !stillOpen {
			// D-189 P0-4 S4-4：broker 已无腿 → 历史回填（无历史则 fail-closed）。
			if err := p.backfillCloseFromHistory(ctx, posID, leg, fx); err != nil {
				return fmt.Errorf("pos=%d leg=%d history backfill: %w", posID, leg.LegNo, err)
			}
			continue
		}
		// broker 仍有腿 → 发平仓单（复用 ClosePosition 的下单逻辑）
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		// D-195-C2 §6.4：部分成交残量继续——计算剩余量（全量时 = leg.Qty）。
		closeQty := leg.Qty
		if closeLeg, hasClose := closeByLegNo[leg.LegNo]; hasClose && closeLeg.FilledQty != nil && *closeLeg.FilledQty > 0 {
			remaining := leg.Qty - *closeLeg.FilledQty
			if remaining > 0 && remaining < closeQty {
				closeQty = remaining
			}
		}
		// D-189 P0-4：fill 来自 preflight（已有成交）；若 preflight 未成交则发新单。
		if fill.OrderID == "" {
			newFill, closeErr := p.placeCloseOrder(ctx, posID, leg, closeSide, closeQty)
			if closeErr != nil {
				log.Printf("[exec] resume close leg %d (ticket=%s venue=%s): %v", leg.ID, leg.OrderID, leg.Venue, closeErr)
				continue
			}
			fill = newFill
		}
		if fill.Qty <= 0 || fill.Price <= 0 {
			msg := fmt.Sprintf("close leg %d invalid fill: qty=%.4f price=%.4f (skip, not faked)", leg.ID, fill.Qty, fill.Price)
			_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
			log.Printf("[exec] %s", msg)
			continue
		}
		closePrice := fill.Price
		closeQty = fill.Qty
		if closeQty <= 0 {
			closeQty = leg.Qty
		}
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         leg.LegNo,
			Phase:         "CLOSE",
			AttemptNo:     1, // D-195-B：首次平仓尝试
			Venue:         leg.Venue,
			Account:       leg.Account,
			Symbol:        leg.Symbol,
			Side:          closeSide,
			Qty:           closeQty,
			PriceExpected: leg.PriceExpected,
			PriceActual:   &closePrice,
			OrderID:       fill.OrderID,
			Status:        "FILLED",
		}); err != nil {
			if errors.Is(err, pgstore.ErrTerminalState) {
				log.Printf("[exec] resume close leg %d already persisted (concurrent), skip", leg.ID)
				continue
			}
			msg := fmt.Sprintf("close leg persist failed position=%d leg=%d venue=%s: %v", posID, leg.LegNo, leg.Venue, err)
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
			return fmt.Errorf("close leg persist: %w", err)
		}
		legPnL := p.calcLegPnL(leg, fill, fx)
		totalPnL += legPnL
		if fxMissing {
			_ = p.store.InsertAlert(ctx, &posID, "critical",
				fmt.Sprintf("pnl fx missing venue=%s symbol=%s quoteCcy=%s", leg.Venue, leg.Symbol, quoteCcy))
		}
		if err := p.writeCloseCashflows(ctx, posID, leg.LegNo, leg.Venue, fill.Swap, fill.Commission, fx); err != nil {
			return fmt.Errorf("pos=%d leg=%d close cashflow: %w", posID, leg.LegNo, err)
		}
	}

	// 残量校验（复用 checkResidualLegs）
	if p.verifier == nil {
		msg := fmt.Sprintf("verifier not wired, cannot verify close position=%d", posID)
		log.Printf("[exec] %s", msg)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		return fmt.Errorf("verifier not wired: position %d", posID)
	}
	residual, verifyErr := p.checkResidualLegs(ctx, legs)
	if verifyErr != nil {
		msg := fmt.Sprintf("verify residual failed position=%d: %v", posID, verifyErr)
		log.Printf("[exec] %s", msg)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		return fmt.Errorf("verify residual: %w", verifyErr)
	}
	if len(residual) > 0 {
		msg := fmt.Sprintf("broker 残留腿 position=%d: %s", posID, strings.Join(residual, ", "))
		log.Printf("[exec] %s", msg)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		return fmt.Errorf("residual legs after close: %s", strings.Join(residual, ", "))
	}

	// PnL 校准
	if p.pnlCalibrator != nil {
		if authPnL, ok := p.calibratePnL(ctx, legs, fx); ok {
			log.Printf("[exec] position %d PnL calibrated: sync=%.2f → auth=%.2f", posID, totalPnL, authPnL)
			totalPnL = authPnL
		} else {
			_ = p.store.SetPositionNote(ctx, posID, "pnl_estimated: partial or no calibration")
		}
	}

	// CLOSE leg 数量完整性校验
	closeLegs, err := p.store.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if err != nil {
		return fmt.Errorf("list close legs: %w", err)
	}
	openFilled, closeFilled := 0, 0
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" {
			openFilled++
		}
	}
	for _, l := range closeLegs {
		if l.Status == "FILLED" {
			closeFilled++
		}
	}
	if openFilled > closeFilled {
		msg := fmt.Sprintf("position %d residual: %d OPEN FILLED legs but only %d CLOSE FILLED legs (not closing)", posID, openFilled, closeFilled)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		log.Printf("[exec] %s", msg)
		return fmt.Errorf("position %d residual legs not closed (%d/%d)", posID, closeFilled, openFilled)
	}

	if err := p.store.ClosePosition(ctx, posID, totalPnL); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already CLOSED (concurrent)", posID)
			return nil
		}
		log.Printf("[exec] close position %d store update: %v", posID, err)
		return fmt.Errorf("close position %d store: %w", posID, err)
	}
	return nil
}
