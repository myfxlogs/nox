// Package reconcile 对账自动收尾流水（D-073+）。
// 「本地腿 venue 未找到」→ 查证 OrderHistory → 命中则收尾（回填 CLOSE leg + 平另一腿 + CLOSED + warn）。
// 处置动作全部走既有执行路径（Closer = service.AutoClosePosition），不新造执行路径。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// Closer 平仓接口（service.AutoClosePosition 实现，reconcile 不依赖 dashboard 包避免循环导入）。
// 走既有 pipeline.ClosePosition 路径：MT5/MT4 按 ticket CloseOrder，CEX 反向减仓。
// 增加 ResumeClose——reconcile CLOSING 超时自动重触发，走 pipeline.ResumeClose 公共执行核心。
// 增加 ClaimClose——auto_close step c 先 ClaimClose（OPEN→CLOSING）再 ResumeClose，
// 修复 position 仍 OPEN 时 ResumeClose 拒绝非 CLOSING 状态导致永久卡死。
type Closer interface {
	AutoClosePosition(ctx context.Context, posID int64) error
	ResumeClose(ctx context.Context, posID int64) error
	ClaimClose(ctx context.Context, posID int64) error
}

// Settler 结算接口（D-087 §3.2，settlement.Settle 的注入面）。
// reconcile 收尾平仓后调 Settle 替换原 StatUpdater + VerificationUpdater 两接口。
type Settler interface {
	Settle(ctx context.Context, posID int64) error
}

// autoClose 收尾单个 position：回填已平腿 + 平另一腿 + CLOSED + 结算 + warn。
// posID=待收尾持仓, goneLegTicket=venue 侧已平的腿 ticket, goneLeg=该腿 leg 记录, hist=历史单查证结果。
// fail-closed：任何一步失败 → 返回 error，调用方插 critical 带原因，下轮重试。
// 缺陷1修复：所有 store/closer 调用包 runTimeout 超时。
// d/d2 统计块替换为 Settle（statUpdater/verificationUpdater 随之简化）。
func (r *Reconciler) autoClose(
	ctx context.Context,
	pos *pgstore.Position,
	goneLeg pgstore.PositionLeg,
	hist *HistoryOrder,
) error {
	posID := pos.ID

	// a. 回填已平腿 CLOSE 记录（取历史单 close price/profit，无则如实空，不伪造）
	closePrice := 0.0
	if hist != nil {
		closePrice = hist.ClosePrice
	}
	closeSide := "SELL"
	if goneLeg.Side == "SELL" {
		closeSide = "BUY"
	}
	sub, cancel := r.withTimeout(ctx)
	err := r.store.CreatePositionLeg(sub, &pgstore.PositionLeg{
		PositionID:    posID,
		LegNo:         goneLeg.LegNo,
		Phase:         "CLOSE",
		AttemptNo:     1, // D-195-B：首次平仓尝试
		Venue:         goneLeg.Venue,
		Account:       goneLeg.Account,
		Symbol:        goneLeg.Symbol,
		Side:          closeSide,
		Qty:           goneLeg.EffectiveQty(), // D-311 R6：回填成交量口径（CEX=基础币）
		PriceExpected: goneLeg.PriceExpected,
		PriceActual:   &closePrice,
		OrderID:       goneLeg.OrderID, // 原 ticket（venue 侧已平，无新单号）
		Status:        "FILLED",
	})
	cancel()
	// ErrTerminalState=该 CLOSE leg 已被并发平仓写入，幂等跳过（不报错继续后续步骤）。
	if err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return fmt.Errorf("回填 CLOSE leg: %w", err)
	}

	// b. 同 position 另一腿仍 OPEN → 自动平掉（走既有 AutoClosePosition 路径，恢复零暴露）
	sub, cancel = r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}
	otherOpen := false
	for _, l := range legs {
		if l.Phase == "OPEN" && l.Status == "FILLED" && l.OrderID != goneLeg.OrderID {
			otherOpen = true
			break
		}
	}
	if otherOpen && r.closer != nil {
		// 缺口2 + D-249 S1 第1项：触发源持久化（reconcile_legged；写失败只 log 不阻断平仓）。
		// reason 增强：含 venue/ticket/History 依据（481 实证——平仓触发源不可回溯）。
		// 字段口径偏差留痕：设计假设 Position 含 canonical/venue_buy/venue_sell/qty，
		// 实际这些在 legs 上（Position 只有 OpportunityID/Type/Venue）——按「空串容忍」填可得字段。
		histClose, histProfit := 0.0, 0.0
		if hist != nil {
			histClose = hist.ClosePrice
			histProfit = hist.Profit
		}
		reason := fmt.Sprintf("reconcile_legged venue=%s ticket=%s symbol=%s hist_close=%.5f hist_profit=%.2f",
			goneLeg.Venue, goneLeg.OrderID, goneLeg.Symbol, histClose, histProfit)
		if err := r.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
			OpportunityID: pos.OpportunityID,
			PositionID:    &posID,
			Strategy:      pos.Type,
			Status:        "CLOSE_TRIGGER",
			Reason:        reason,
		}); err != nil {
			log.Printf("[reconcile] D-245 CLOSE_TRIGGER 落库失败 pos=%d: %v", posID, err)
		}
		// AutoClosePosition 会平掉所有 FILLED 腿（含已回填 CLOSE 的那条——但那条 phase=CLOSE 会被跳过）
		// 实际上 pipeline.ClosePosition 遍历所有 FILLED 腿按 venue 平——已平腿 CloseOrder 会失败但 continue 不阻断
		// 更安全：只平另一条 OPEN 腿。但既有路径是整 position 平仓，复用不新造。
		sub, cancel := r.withTimeout(ctx)
		err := r.closer.AutoClosePosition(sub, posID)
		cancel()
		if err != nil {
			return fmt.Errorf("自动平另一腿: %w", err)
		}
	}

	// c. position 置 CLOSED + realized_pnl 汇总
	// 统一走 close coordinator（不独立置 CLOSED）。
	// reconcile 收尾不再绕过执行管线直接调 store.ClosePosition / raw cashflow；
	// 改为调用 Closer.ResumeClose，由执行管线按同一套 USD/现金流/残量规则处理。
	// auto_close 的 position 仍 OPEN（来自 ListPositions("OPEN")），
	// ResumeClose 只接受 CLOSING → 先 ClaimClose（OPEN→CLOSING）再 ResumeClose。
	// ClaimClose ErrClaimFailed=已被并发领取为 CLOSING → 仍尝试 ResumeClose（幂等）。
	if !otherOpen {
		sub, cancel := r.withTimeout(ctx)
		err := r.closer.ClaimClose(sub, posID)
		cancel()
		if err != nil && !errors.Is(err, pgstore.ErrClaimFailed) {
			return fmt.Errorf("close coordinator (ClaimClose): %w", err)
		}
		sub, cancel = r.withTimeout(ctx)
		err = r.closer.ResumeClose(sub, posID)
		cancel()
		if err != nil && !errors.Is(err, pgstore.ErrClaimFailed) && !errors.Is(err, pgstore.ErrTerminalState) {
			return fmt.Errorf("close coordinator (ResumeClose): %w", err)
		}
	}

	// d. D-087 §3.2：settlement.Settle 替换原 d（StatUpdater）+ d2（VerificationUpdater）两块
	// settlement 失败 → 标记 settle_failed 到 position.note（可重试状态，不只打日志）。
	if r.settler != nil {
		sub, cancel := r.withTimeout(ctx)
		err := r.settler.Settle(sub, posID)
		cancel()
		if err != nil {
			log.Printf("[reconcile] settle pos=%d: %v", posID, err)
			noteSub, noteCancel := r.withTimeout(ctx)
			_ = r.store.SetPositionNote(noteSub, posID, "settle_failed: "+err.Error())
			noteCancel()
		}
	}

	// 可见性：warn 级一次性告警
	otherNote := "另一腿已平"
	if !otherOpen {
		otherNote = "无需平（无另一 OPEN 腿）"
	}
	msg := fmt.Sprintf("已自动对账收尾 position #%d（venue 侧已平：ticket=%s），%s", posID, goneLeg.OrderID, otherNote)
	r.insertAlertCtx(ctx, "warn", msg)
	log.Printf("[reconcile] %s", msg)
	return nil
}

// logResumeClose D-249 S1 第2项：ResumeClose 重触发持久化（481 实证；每次调用都记；写失败只 log 不阻断，同 gate.go:263）。
func (r *Reconciler) logResumeClose(ctx context.Context, p *pgstore.Position, stuckFor time.Duration) {
	pid := p.ID
	if err := r.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: p.OpportunityID, PositionID: &pid, Strategy: p.Type, Status: "RESUME_CLOSE",
		Reason: fmt.Sprintf("resume_close stuck_for=%s pos=#%d", stuckFor.Round(time.Minute), pid),
	}); err != nil {
		log.Printf("[reconcile] D-249 RESUME_CLOSE 落库失败 pos=%d: %v", pid, err)
	}
}
