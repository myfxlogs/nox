// pipeline_resume.go —— D-189 P0-4：ResumeClose 公共执行核心 + per-position close 锁。
// 从 pipeline_legs_close.go 拆出（pipeline.go >300 行软警告）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// ResumeClose D-189 P0-4：重触发 CLOSING 持仓的平仓流程（reconcile 超时自动重试入口）。
// 幂等：CLOSED/FAILED 直接返回 nil；只有 CLOSING 进入核心；其他状态返回 error。
// 不调 ClaimClose（position 已是 CLOSING）；走与 ClosePosition 相同的 leg 处理 + 残量校验 + PnL 汇总 + CLOSED。
// D-407 S1：per-position try-lock（锁挂本入口=三调用方统一收敛）；持锁中 → ErrCloseInFlight。
func (p *Pipeline) ResumeClose(ctx context.Context, posID int64) error {
	if p.store == nil {
		return fmt.Errorf("pipeline store not wired")
	}
	unlock, ok := p.tryLockClose(posID)
	if !ok {
		return fmt.Errorf("%w pos=%d", ErrCloseInFlight, posID)
	}
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
	return p.closePositionCore(ctx, posID, false)
}

// closePositionCore D-189 P0-4：共享平仓核心（ClosePosition 与 ResumeClose 共用）。
// D-495：force=true（ClosePositionForce 唯一来源）跳过 per-leg 可平性检查——
// 逃逸门语义=明知单腿裸露也要平，per-leg 跳过只在非 force 路径生效。
// 职责：leg 处理（发平仓单/历史回填）→ 残量校验 → PnL 校准 → 现金流完整性 → CLOSED。
// legNo 去重：closeByLegNo map（废弃 closedTickets[OrderID]）。
// broker preflight：对无 CLOSE leg 的 OPEN 腿查 verifier；broker 已无腿则历史回填，无历史则 fail-closed。
// 现金流完整性：已有 CLOSE leg 的腿检查 cashflows 缺失，从权威历史补回。
func (p *Pipeline) closePositionCore(ctx context.Context, posID int64, force bool) error {
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

	// D-539：全腿前置闸——任一腿不可行动本轮整仓零发单（pos502 形态根治），force 逃逸门全绕。
	if !force {
		if err := p.legsActionableGate(ctx, posID, legs, &pos.Note); err != nil {
			return err
		}
	}

	var totalPnL float64
	// D-495 S4：await_session 跟踪——不可行动腿跳过发单（不写 CLOSE leg、不 error 中断），
	// token 合并写 note；残留校验命中全为 await 腿时降级 warn（关市常态非异常）。
	var awaited []pgstore.PositionLeg
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
			if !hasPartialFill || closeFilledQty >= leg.EffectiveQty() {
				// 无部分成交（nil/0=未成交或全量）或已全部平仓 → cashflows 补回后跳过。
				if err := p.backfillMissingCashflows(ctx, posID, leg, fx); err != nil {
					return fmt.Errorf("pos=%d leg=%d backfill cashflow: %w", posID, leg.LegNo, err)
				}
				continue
			}
			// 部分成交（0 < filled_qty < open_qty）→ 继续对剩余量发单（不跳过本腿）
			// D-311 R6：比对同单位——filled_qty 与 EffectiveQty 均为成交口径（CEX=基础币）。
			log.Printf("[exec] resume close leg %d partial fill: openQty=%.4f closeFilledQty=%.4f remaining=%.4f",
				leg.ID, leg.EffectiveQty(), closeFilledQty, leg.EffectiveQty()-closeFilledQty)
		}
		// D-495 S4：逐腿可平性闸——不可行动腿跳过发单（不写 CLOSE leg、不 error 中断，
		// ResumeClose 周期重试自愈=pos502 型休市残腿开市自动补平；其余腿善后照常）。
		if ok, why := legActionableUnlessForce(ctx, p.store, p.tradeModeProber(leg.Venue), leg, force); !ok {
			log.Printf("[exec] resume close leg %d await_session %s@%s: %s", leg.ID, leg.Symbol, leg.Venue, why)
			awaited = append(awaited, leg)
			continue
		}
		// D-189 P0-4 S4-2：broker preflight——查 verifier 确认腿是否仍在仓。
		stillOpen, fill, err := p.brokerPreflight(ctx, posID, leg)
		if err != nil {
			return fmt.Errorf("pos=%d leg=%d broker preflight: %w", posID, leg.LegNo, err)
		}
		if !stillOpen {
			// D-189 P0-4 S4-4：broker 已无腿 → 历史回填（无历史则 fail-closed）。
			if err := p.backfillCloseFromHistory(ctx, pos, leg, fx); err != nil {
				return fmt.Errorf("pos=%d leg=%d history backfill: %w", posID, leg.LegNo, err)
			}
			continue
		}
		// broker 仍有腿 → 发平仓单（复用 ClosePosition 的下单逻辑）
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		// D-195-C2 §6.4：部分成交残量继续——计算剩余量（全量时 = 实际持仓量）。
		// D-311 R6：EffectiveQty=成交口径（CEX=基础币），与 closeLeg.FilledQty 同单位。
		closeQty := leg.EffectiveQty()
		if closeLeg, hasClose := closeByLegNo[leg.LegNo]; hasClose && closeLeg.FilledQty != nil && *closeLeg.FilledQty > 0 {
			remaining := leg.EffectiveQty() - *closeLeg.FilledQty
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
			msg := fmt.Sprintf("平仓腿 %d 成交回报无效（量 %.4f 价 %.4f），已跳过不虚增", leg.ID, fill.Qty, fill.Price)
			if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
				log.Printf("[exec] pos %d alert write: %v", posID, err)
			}
			log.Printf("[exec] %s", msg)
			continue
		}
		closePrice := fill.Price
		closeQty = fill.Qty
		if closeQty <= 0 {
			closeQty = leg.EffectiveQty() // D-311 R6：兜底用成交口径（CEX=基础币）
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
			FilledQty:     &closeQty,                // D-346：与 OPEN 腿同字段语义，realized 逐腿分解复核
			AvgFillPrice:  &closePrice,              // D-346：488/489/490 验尸=CLOSE 腿恒 NULL
			FeeUSD:        closeLegFeeUSD(fill, fx), // D-422+D-599：CEX 实采 USD 费/MT5 commission 落库（0→NULL）
		}); err != nil {
			if errors.Is(err, pgstore.ErrTerminalState) {
				log.Printf("[exec] resume close leg %d already persisted (concurrent), skip", leg.ID)
				continue
			}
			msg := fmt.Sprintf("持仓 #%d 平仓腿落库失败（第 %d 腿 %s）：%v", posID, leg.LegNo, leg.Venue, err)
			log.Printf("[exec] %s", msg)
			if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
				log.Printf("[exec] pos %d alert write: %v", posID, err)
			}
			return fmt.Errorf("close leg persist: %w", err)
		}
		legPnL := p.calcLegPnL(leg, fill, fx)
		totalPnL += legPnL
		if fxMissing {
			if err := p.store.InsertPositionAlert(ctx, posID, "critical",
				fmt.Sprintf("盈亏汇率缺失（%s %s 计价币种 %s）——盈亏按未折算记", leg.Venue, leg.Symbol, quoteCcy)); err != nil {
				log.Printf("[exec] pos %d alert write: %v", posID, err)
			}
		}
		if err := p.writeCloseCashflows(ctx, posID, leg.LegNo, leg.Venue, fill.Swap, fill.Commission, fill.FeeUSD, fx); err != nil {
			return fmt.Errorf("pos=%d leg=%d close cashflow: %w", posID, leg.LegNo, err)
		}
	}

	// D-495 S4：await_session token 合并写（SetPositionNote=整字段覆盖，
	// mergeNoteToken 保 settle_failed:/pnl_estimated:/PROBE 等异类 token）。
	newAwait := p.writeAwaitSessionNote(ctx, posID, &pos.Note, awaited)

	// 残量校验（复用 checkResidualLegs）
	if err := p.verifyResidualOrErr(ctx, posID, legs, awaited, newAwait); err != nil {
		return err
	}

	// D-408 S1：realized_pnl=持久化 CLOSE 腿汇总（单一事实源=落库腿数据）——
	// 循环内累加漏前次已平腿/回填腿（两条 continue 跳过，498 实证 −1.42→−12.36）。
	// closeLegs 重读在循环后：回填腿已落库在此可见（内存 closeByLegNo 不含回填腿）。
	closeLegs, err := p.store.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if err != nil {
		return fmt.Errorf("list close legs: %w", err)
	}
	totalPnL = p.sumClosedLegsPnL(ctx, posID, legs, closeLegs, fx)

	// PnL 校准（D-422：est=有可校准腿但未全部命中才写 pnl_estimated；纯 CEX 仓不写）
	if p.pnlCalibrator != nil {
		if authPnL, ok, est := p.calibratePnL(ctx, legs, closeLegs, fx); ok {
			log.Printf("[exec] position %d PnL calibrated: sync=%.2f → auth=%.2f", posID, totalPnL, authPnL)
			totalPnL = authPnL
		} else if est {
			_ = p.store.SetPositionNote(ctx, posID, "pnl_estimated: partial or no calibration")
		}
	}

	// CLOSE leg 数量完整性校验
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
		msg := fmt.Sprintf("持仓 #%d 平仓不完整：%d 条开仓腿已成交但仅 %d 条平仓腿成交——需人工核对", posID, openFilled, closeFilled)
		if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, err)
		}
		log.Printf("[exec] %s", msg)
		return fmt.Errorf("position %d residual legs not closed (%d/%d)", posID, closeFilled, openFilled)
	}

	// D-599 S2：净额核算——totalPnL 为价差毛值；fees_usd=全腿实采+估算费合计，
	// fee_quality 四分位（actual/estimated/mixed/none）；净额=毛值−fees_usd 原子落库。
	feesUSD, feeQuality := p.positionFeesUSD(legs, closeLegs, fx)
	netPnL := totalPnL
	if feesUSD != nil {
		netPnL -= *feesUSD
	}

	if err := p.store.ClosePosition(ctx, posID, netPnL, feesUSD, feeQuality); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already CLOSED (concurrent)", posID)
			return nil
		}
		log.Printf("[exec] close position %d store update: %v", posID, err)
		return fmt.Errorf("close position %d store: %w", posID, err)
	}
	// D-249 S1 第3项：CLOSED 翻译点记录（裁定 a385b84）。
	// 成功（非 ErrTerminalState）才写；禁预查（TOCTOU）；best-effort 不阻断。
	p.logClosed(ctx, pos, closeLegs, netPnL) // D-599：日志同净额口径（realized_pnl=net）
	// D-193-A 扩包 S5：CLOSED 后触发 settlement.Settle（修复 479 缺 settlement 行根因）。
	// settler nil=未接线（兼容旧测试）；失败不回滚 CLOSED（settlement 可重试，SettleAt 幂等）。
	if p.settler != nil {
		if err := p.settler.Settle(ctx, posID); err != nil {
			log.Printf("[exec] position %d settle after close: %v (settlement 可重试)", posID, err)
		}
	}
	return nil
}
