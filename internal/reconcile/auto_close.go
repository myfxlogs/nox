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
type Closer interface {
	AutoClosePosition(ctx context.Context, posID int64) error
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
// D-087 §3.2：d/d2 统计块替换为 Settle（statUpdater/verificationUpdater 随之简化）。
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
		Venue:         goneLeg.Venue,
		Account:       goneLeg.Account,
		Symbol:        goneLeg.Symbol,
		Side:          closeSide,
		Qty:           goneLeg.Qty,
		PriceExpected: goneLeg.PriceExpected,
		PriceActual:   &closePrice,
		OrderID:       goneLeg.OrderID, // 原 ticket（venue 侧已平，无新单号）
		Status:        "FILLED",
	})
	cancel()
	// D-159 W1a R3-1：ErrTerminalState=该 CLOSE leg 已被并发平仓写入，幂等跳过（不报错继续后续步骤）。
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
	// D-159 W2a R3-L174：统一走 close coordinator（不独立置 CLOSED）。
	// auto_close 已回填 CLOSE leg（step a），另一腿无 OPEN（otherOpen=false），
	// 走 ClosePosition 做残量校验 + PnL 汇总 + CLOSED（ClaimClose 从 OPEN/LEGGED 领取，
	// 但此处 position 可能已是 CLOSING——ClaimClose 会 ErrClaimFailed，需兜底）。
	if !otherOpen {
		sub, cancel := r.withTimeout(ctx)
		err := r.closer.AutoClosePosition(sub, posID)
		cancel()
		if err != nil && !errors.Is(err, pgstore.ErrClaimFailed) && !errors.Is(err, pgstore.ErrTerminalState) {
			return fmt.Errorf("close coordinator: %w", err)
		}
		// ClaimFailed=已是 CLOSING（auto_close step a 已回填 CLOSE leg，position 可能已被并发领取），
		// 用 store.ClosePosition 兜底（残量校验在 ClosePosition 内做，此处只回填 PnL）
		if errors.Is(err, pgstore.ErrClaimFailed) {
			pnl := 0.0
			if hist != nil {
				// D-159 W3a R4：权威值只用价格 PnL（Profit），Swap/Commission 走 cashflows（双计拆除）。
				pnl = hist.Profit
			}
			sub, cancel := r.withTimeout(ctx)
			err := r.store.ClosePosition(sub, posID, pnl)
			cancel()
			if err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
				return fmt.Errorf("close position store (claim failed fallback): %w", err)
			}
			// D-159 W3a R4：补写 swap/commission cashflows（兜底分支不走 Pipeline.ClosePosition，
			// S2 后 realized_pnl 不再含 swap/commission，不补写则永久丢失）。legNo 用 goneLeg.LegNo。
			// hist.Swap/Commission 是 MT 返回原始值（reconcile 层无 fxResolver，与旧路径同语义）——
			// 非 USD 计价腿的 fx 换算缺口上报决策方（见施工报告观察项）。
			if hist != nil && (hist.Swap != 0 || hist.Commission != 0) {
				sub2, cancel2 := r.withTimeout(ctx)
				if hist.Swap != 0 {
					_ = r.store.InsertCashflow(sub2, posID, goneLeg.LegNo, "swap", goneLeg.Venue, hist.Swap, time.Now())
				}
				if hist.Commission != 0 {
					_ = r.store.InsertCashflow(sub2, posID, goneLeg.LegNo, "commission", goneLeg.Venue, -hist.Commission, time.Now())
				}
				cancel2()
			}
		}
	}

	// d. D-087 §3.2：settlement.Settle 替换原 d（StatUpdater）+ d2（VerificationUpdater）两块
	// D-159 W2a R3-9：settlement 失败 → 标记 settle_failed 到 position.note（可重试状态，不只打日志）。
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

// autoFailOPENINGZombie OPENING 僵尸收编（超 24h）：腿在 venue 有则照收尾，无则 FAILED + warn。
// D-108 A3：venue 命中（腿在 venue 真有持仓）→ 不置 FAILED（记录告警，交人工/下轮）；
// 未命中才置 FAILED。原死代码 venue 命中后 break → 无条件置 FAILED=误杀真持仓。
func (r *Reconciler) autoFailOPENINGZombie(
	ctx context.Context,
	pos *pgstore.Position,
	venueOrders map[string]VenueOrder,
) error {
	posID := pos.ID
	sub, cancel := r.withTimeout(ctx)
	legs, err := r.store.ListPositionLegs(sub, posID)
	cancel()
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}
	// 查是否有腿在 venue 存在
	for _, leg := range legs {
		if leg.OrderID == "" {
			continue
		}
		// D-159 W2a R3-7：复合 key（venue_key+":"+ticket）查 venueOrders。
		if _, ok := venueOrders[leg.Venue+":"+leg.OrderID]; ok {
			// D-108 A3：腿在 venue 有 → 不置 FAILED（venue 真有持仓，误杀风险），
			// 记录告警交人工/下轮对账处置。
			msg := fmt.Sprintf("OPENING 僵尸 position #%d 腿 ticket=%s 在 venue 有持仓，不归档 FAILED（交人工/对账）", posID, leg.OrderID)
			r.insertAlertCtx(ctx, "warn", msg)
			log.Printf("[reconcile] %s", msg)
			return nil
		}
	}
	// OPENING 僵尸无有效持仓 → FAILED
	sub, cancel = r.withTimeout(ctx)
	err = r.store.UpdatePositionStatus(sub, posID, "FAILED")
	cancel()
	if err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return fmt.Errorf("update status FAILED: %w", err)
	}
	msg := fmt.Sprintf("OPENING 僵尸自动归档 position #%d（超 24h 未完成开仓，转 FAILED）", posID)
	r.insertAlertCtx(ctx, "warn", msg)
	log.Printf("[reconcile] %s", msg)
	return nil
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
	venueOrders map[string]VenueOrder,
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
		// D-159 W2a R3-7：复合 key（venue_key+":"+ticket）查 venueOrders。
		if _, ok := venueOrders[leg.Venue+":"+leg.OrderID]; ok {
			// D-108 P2 T3：FILLED 腿在 venue 有 → 不置 FAILED（venue 真有持仓，误平风险），
			// 记录告警交人工/下轮对账处置（P1 A3 同语义）。
			msg := fmt.Sprintf("LEGGED 僵尸 position #%d 腿 ticket=%s 在 venue 有持仓，不归档 FAILED（交人工/对账）", posID, leg.OrderID)
			r.insertAlertCtx(ctx, "warn", msg)
			log.Printf("[reconcile] %s", msg)
			return nil
		}
	}
	// LEGGED 僵尸无 FILLED 腿在 venue → FAILED 归档
	sub, cancel = r.withTimeout(ctx)
	err = r.store.UpdatePositionStatus(sub, posID, "FAILED")
	cancel()
	if err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return fmt.Errorf("update status FAILED: %w", err)
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
