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
		}); err != nil {
			msg := fmt.Sprintf("leg persist failed position=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, err)
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, nil, "critical", msg)
			// D-201：补偿回滚已 FILLED 腿（0~i），best-effort（回滚失败只 log+alert，不阻断返回原 error）。
			p.rollbackFilledLegs(ctx, posID, legs, results, i)
			return fmt.Errorf("leg persist: %w", err)
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
			closeSide := "SELL"
			if legs[i].Side == "SELL" {
				closeSide = "BUY"
			}
			// D-249 S4 批3 第1项：回滚单补 PositionSide（对齐 placeCloseOrder/auto-flatten）。
			// 审计 P1-1：回滚单不带 PositionSide → long_short_mode 账户回滚 long 会新开 short（双倍敞口）。
			// PositionSide=原持仓侧（leg.Side 推导，与 placeCloseOrder 同款），CEX 适配器据此设 reduceOnly/posSide。
			var positionSide string
			switch legs[i].Side {
			case "BUY":
				positionSide = "long"
			case "SELL":
				positionSide = "short"
			}
			// D-311 R4：回滚单同执行器边界——:perp 腿合约数→基础币（不换算会 1/cs 倍超量回滚）。
			orderQty, convErr := p.orderQtyFor(legs[i])
			if convErr != nil {
				closeErr = convErr
			} else {
				_, closeErr = exe.PlaceOrder(ctx, Order{
					Symbol: legs[i].VenueSymbol, Side: closeSide, Qty: orderQty, Venue: legs[i].VenueKey,
					PositionSide: positionSide,
				})
			}
		}
		if closeErr != nil {
			rmsg := fmt.Sprintf("rollback failed pos=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, closeErr)
			log.Printf("[exec] %s", rmsg)
			_ = p.store.InsertAlert(ctx, nil, "critical", rmsg)
		} else {
			log.Printf("[exec] rollback ok pos=%d leg=%d venue=%s ticket=%s", posID, i, legs[i].VenueKey, r.OrderID)
		}
	}
}
