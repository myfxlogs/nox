package exec

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// persistOpenLegs D-195-B：OPEN leg 写入循环（从 Pipeline.Execute 提取，避免 pipeline.go 超 300 行）。
// D-108 A1 fail-closed：腿写库失败 → error+critical 告警，孤儿不可留。
// D-195-B §4.3：client_order_id 确定性编码 + attempt 字段填充。
func (p *Pipeline) persistOpenLegs(ctx context.Context, requestID string, posID int64, legs []LegSpec, results []LegResult) error {
	for i, r := range results {
		clientOrderID := generateClientOrderID(requestID, posID, int16(i), 1) // attempt_no=1 首次
		now := time.Now()
		var uncertainAt *time.Time
		if r.Status == "UNKNOWN" {
			uncertainAt = &now
		}
		filledQty := r.Qty
		avgPrice := r.Price
		// D-391 S2：fee_usd 采集补洞（declared-but-never-wired 收编）。正数成本口径：
		// CEX 走 Fill.FeeUSD（okx order 响应 fee 实证）；MT 腿顺手用 broker 回报
		// commission（负=付出→取绝对值）。拿不到 → NULL（不写 0 冒充无费）。
		fee := r.FeeUSD
		if fee <= 0 && r.Commission < 0 {
			fee = -r.Commission
		}
		var feeUSD *float64
		if fee > 0 {
			feeUSD = &fee
		}
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         int16(i),
			Phase:         "OPEN",
			AttemptNo:     1,
			Venue:         legs[i].VenueKey,
			Symbol:        legs[i].VenueSymbol,
			Side:          legs[i].Side,
			Qty:           legs[i].Qty,
			PriceExpected: legs[i].PriceExpected,
			PriceActual:   &r.Price,
			OrderID:       r.OrderID,
			Status:        r.Status,
			ClientOrderID: clientOrderID,
			RequestedQty:  &legs[i].Qty,
			FilledQty:     &filledQty,
			AvgFillPrice:  &avgPrice,
			SubmittedAt:   &now,
			UncertainAt:   uncertainAt,
			FeeUSD:        feeUSD,
		}); err != nil {
			msg := fmt.Sprintf("leg persist failed position=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, err)
			log.Printf("[exec] %s", msg)
			p.alertCrit(ctx, msg)
			// D-201：补偿回滚已 FILLED 腿（0~i），best-effort（回滚失败只 log+alert，不阻断返回原 error）。
			p.rollbackFilledLegs(ctx, posID, legs, results, i)
			return fmt.Errorf("leg persist: %w", err)
		}
		// D-422 §1.4：OPEN 腿费同点入账（入场费不再只挂腿行）——kind=commission 负值，
		// 幂等键 event_key 精确匹配（重试 HasCashflowByEventKey 拦截不双计，D-422 R1）。失败不阻断开仓，记 critical。
		if feeUSD != nil {
			ts := now.UTC()
			dayStart := time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
			// dedupTag="open"：与同日同腿 CLOSE 佣金（tag=""）分键不互吞（D-422 R1）；
			// quality='actual'（Fill/Commission 实采，D-425）。
			if err := p.insertCashflowIdempotent(ctx, posID, int16(i), "commission", legs[i].VenueKey, -*feeUSD, "actual", dayStart, ts, "open"); err != nil {
				log.Printf("[exec] pos=%d leg=%d open fee cashflow: %v", posID, i, err)
				p.alertCrit(ctx,
					fmt.Sprintf("position %d leg %d open fee cashflow write failed: %v", posID, i, err))
			}
		}
	}
	return nil
}

// rollbackFilledLegs D-201：persistOpenLegs 失败时补偿回滚所有 FILLED 腿（0~len(results)-1）。
// best-effort：回滚失败只 log+alert，不阻断。
// MT 腿用 CloseOrder（按 ticket），CEX 腿用 PlaceOrder 反向。
// UNKNOWN/FAILED 腿不回滚（UNKNOWN 可能已成交需人工确认，FAILED 未成交）。
// D-201 复审修正：原设计只回滚 0~failedIdx，但 persistOpenLegs 在第一个失败处 return，
// failedIdx 之后的腿已在 broker 端并发执行（pipeline.go wg.Wait 后全部完成）——
// 既未 persist 也未回滚 → 仍是孤儿。扩展回滚范围至全部 results。
func (p *Pipeline) rollbackFilledLegs(ctx context.Context, posID int64, legs []LegSpec, results []LegResult, failedIdx int) {
	_ = failedIdx // D-201 复审修正：保留签名兼容，实际回滚全部 results
	for i := 0; i < len(results); i++ {
		r := results[i]
		if r.Status == "UNKNOWN" && r.OrderID != "" {
			p.rollbackUnknownLeg(ctx, posID, i, legs[i], r)
			continue
		}
		if r.Status != "FILLED" {
			continue
		}
		exe, ok := p.registry.Get(legs[i].VenueKey)
		if !ok {
			log.Printf("[exec] rollback pos=%d leg=%d: no executor for %s", posID, i, legs[i].VenueKey)
			continue
		}
		var closeErr error
		if isMTVenueKey(legs[i].VenueKey) {
			_, closeErr = exe.CloseOrder(ctx, OrderRef{OrderID: r.OrderID, Venue: legs[i].VenueKey})
		} else {
			// D-311 R4：回滚单同执行器边界——:perp 腿合约数→基础币（不换算会 1/cs 倍超量回滚）。
			orderQty, convErr := p.orderQtyFor(legs[i])
			if convErr != nil {
				closeErr = convErr
			} else {
				closeErr = p.rollbackCEXPlace(ctx, exe, legs[i], orderQty)
			}
		}
		if closeErr != nil {
			rmsg := fmt.Sprintf("rollback failed pos=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, closeErr)
			log.Printf("[exec] %s", rmsg)
			p.alertCrit(ctx, rmsg)
		} else {
			log.Printf("[exec] rollback ok pos=%d leg=%d venue=%s ticket=%s", posID, i, legs[i].VenueKey, r.OrderID)
		}
	}
}

// rollbackCEXPlace CEX 反向平仓单（FILLED 回滚与 UNKNOWN 部分成交回滚共用，D-427 I2）。
// qty=基础币数量（调用方负责换算）；PositionSide=原持仓侧（leg.Side 推导，对齐 placeCloseOrder）。
func (p *Pipeline) rollbackCEXPlace(ctx context.Context, exe Executor, leg LegSpec, qty float64) error {
	closeSide := "SELL"
	if leg.Side == "SELL" {
		closeSide = "BUY"
	}
	positionSide := ""
	switch leg.Side {
	case "BUY":
		positionSide = "long"
	case "SELL":
		positionSide = "short"
	}
	_, err := exe.PlaceOrder(ctx, Order{
		Symbol: leg.VenueSymbol, Side: closeSide, Qty: qty, Venue: leg.VenueKey,
		PositionSide: positionSide,
	})
	return err
}

// rollbackUnknownLeg D-427 I2：UNKNOWN+OrderID 腿孤儿出证。
// CEX：cancel → 终查 QueryOrderStatus——qty>0=真实部分量走同一反向平仓路径；=0=确定未成交干净跳过；
// cancel/终查失败 → log+critical（孤儿未证否留人工），不阻断主循环。
// MT：消解器 owns（市价单无 cancel 语义），只 log 跳过。
func (p *Pipeline) rollbackUnknownLeg(ctx context.Context, posID int64, i int, leg LegSpec, r LegResult) {
	if isMTVenueKey(leg.VenueKey) {
		log.Printf("[exec] rollback pos=%d leg=%d: MT UNKNOWN order=%s skip（消解器 owns）", posID, i, r.OrderID)
		return
	}
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		log.Printf("[exec] rollback pos=%d leg=%d: no executor for %s", posID, i, leg.VenueKey)
		return
	}
	canceler, ok := exe.(OrderCanceler)
	if !ok {
		log.Printf("[exec] rollback pos=%d leg=%d venue=%s: executor 无 CancelOrder，孤儿单未出证", posID, i, leg.VenueKey)
		return
	}
	fail := func(what string, err error) {
		msg := fmt.Sprintf("rollback orphan pos=%d leg=%d venue=%s order=%s %s failed: %v（孤儿未证否）", posID, i, leg.VenueKey, r.OrderID, what, err)
		log.Printf("[exec] %s", msg)
		p.alertCrit(ctx, msg)
	}
	if err := canceler.CancelOrder(ctx, OrderRef{OrderID: r.OrderID, Venue: leg.VenueKey, Symbol: leg.VenueSymbol}); err != nil {
		fail("cancel", err)
		return
	}
	qs, ok := exe.(OrderStatusQuerier)
	if !ok {
		fail("final-query", fmt.Errorf("executor 无 OrderStatusQuerier"))
		return
	}
	_, qty, _, _, err := qs.QueryOrderStatus(ctx, r.OrderID, "", leg.VenueSymbol)
	if err != nil {
		fail("final-query", err)
		return
	}
	if qty <= 0 {
		log.Printf("[exec] rollback pos=%d leg=%d venue=%s order=%s canceled zero-fill（孤儿证否）", posID, i, leg.VenueKey, r.OrderID)
		return
	}
	// 撤单前已部分成交 → 真实敞口走与 FILLED 同一反向平仓路径（qty 已为基础币）。
	if err := p.rollbackCEXPlace(ctx, exe, leg, qty); err != nil {
		fail("reverse-close", err)
		return
	}
	log.Printf("[exec] rollback pos=%d leg=%d venue=%s order=%s partial qty=%.8g reversed", posID, i, leg.VenueKey, r.OrderID, qty)
}

// alertCrit D-508 审计契约：critical 告警写——写失败只落 log 防递归（不得再告警）。
func (p *Pipeline) alertCrit(ctx context.Context, msg string) {
	if err := p.store.InsertAlert(ctx, nil, "critical", msg); err != nil {
		log.Printf("[exec] alert write failed: %v | original: %s", err, msg)
	}
}
