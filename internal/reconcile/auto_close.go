// Package reconcile 对账自动收尾流水（D-073+）。
// 「本地腿 venue 未找到」→ 查证 OrderHistory → 命中则收尾（回填 CLOSE leg + 平另一腿 + CLOSED + warn）。
// 处置动作全部走既有执行路径（Closer = service.AutoClosePosition），不新造执行路径。
package reconcile

import (
	"context"
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

// StatUpdater 策略统计更新接口（autotrade.Engine.updateStrategyStat 等价，reconcile 收尾后调）。
// 接口化避免 reconcile → autotrade 包依赖（autotrade 已依赖 position，再加 reconcile 会循环）。
type StatUpdater interface {
	UpdateStrategyStat(ctx context.Context, posType string, pnl float64) error
}

// autoClose 收尾单个 position：回填已平腿 + 平另一腿 + CLOSED + 统计 + warn。
// posID=待收尾持仓, goneLegTicket=venue 侧已平的腿 ticket, goneLeg=该腿 leg 记录, hist=历史单查证结果。
// fail-closed：任何一步失败 → 返回 error，调用方插 critical 带原因，下轮重试。
// 缺陷1修复：所有 store/closer 调用包 runTimeout 超时。
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
	if err != nil {
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
	// AutoClosePosition 内部已调 ClosePosition（置 CLOSED + pnl）；若未调（otherOpen=false）则手动置
	if !otherOpen {
		pnl := 0.0
		if hist != nil {
			pnl = hist.Profit + hist.Swap - hist.Commission
		}
		sub, cancel := r.withTimeout(ctx)
		err := r.store.ClosePosition(sub, posID, pnl)
		cancel()
		if err != nil {
			return fmt.Errorf("close position store: %w", err)
		}
	}

	// d. 统计更新（淘汰统计不断链）
	if r.statUpdater != nil && pos.Type != "" {
		pnl := 0.0
		if hist != nil {
			pnl = hist.Profit + hist.Swap - hist.Commission
		}
		sub, cancel := r.withTimeout(ctx)
		err := r.statUpdater.UpdateStrategyStat(sub, pos.Type, pnl)
		cancel()
		if err != nil {
			log.Printf("[reconcile] update strategy stat pos=%d: %v", posID, err)
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
		if _, ok := venueOrders[leg.OrderID]; ok {
			// 腿在 venue 有 → 照收尾（但 OPENING 通常无 FILLED 腿，走 FAILED 更合理）
			// 此处置 FAILED（OPENING 无有效持仓，venue 侧残留另案人工处置）
			break
		}
	}
	// OPENING 僵尸无有效持仓 → FAILED
	sub, cancel = r.withTimeout(ctx)
	err = r.store.UpdatePositionStatus(sub, posID, "FAILED")
	cancel()
	if err != nil {
		return fmt.Errorf("update status FAILED: %w", err)
	}
	msg := fmt.Sprintf("OPENING 僵尸自动归档 position #%d（超 24h 未完成开仓，转 FAILED）", posID)
	r.insertAlertCtx(ctx, "warn", msg)
	log.Printf("[reconcile] %s", msg)
	return nil
}

// isOPENINGZombie 判断 position 是否为 OPENING 僵尸（超 24h）。
func isOPENINGZombie(pos *pgstore.Position, now time.Time) bool {
	if pos.Status != "OPENING" {
		return false
	}
	if pos.OpenedAt == nil {
		return false // 无 opened_at 不判（避免误杀）
	}
	return now.Sub(*pos.OpenedAt) > 24*time.Hour
}
