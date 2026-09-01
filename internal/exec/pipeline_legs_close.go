package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// ClosePosition 平仓（§13.3）。
// D-068 修复：MT5 hedging 模式下 PlaceOrder 反向下单 = 开新仓不是平仓。
// MT5/MT4 腿用 CloseOrder（OrderClose 按 ticket 平仓）；CEX 腿用 PlaceOrder 反向减仓（D-059）。
func (p *Pipeline) ClosePosition(ctx context.Context, posID int64) error {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}

	// D-159 W1a R3-1：原子领取平仓权（只允许 OPEN/LEGGED→CLOSING，防并发重复领取）。
	if err := p.store.ClaimClose(ctx, posID); err != nil {
		if errors.Is(err, pgstore.ErrClaimFailed) {
			log.Printf("[exec] position %d close claim failed (not OPEN/LEGGED, concurrent claim?): %v", posID, err)
			return err
		}
		log.Printf("[exec] position %d ClaimClose: %v", posID, err)
		return err
	}

	// D-105 T2：计价币→USD 换算——查 position 取 canonical → notional.QuoteCcy → fxResolver.USDPer。
	// quoteCcy=""（未知，指数/商品 CFD）或 "USD" → fx=1（既有约定）；非 USD 且 fx 缺失 → fx=0 + critical alert。
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

	// D-159 W2a R3-3：已存在 CLOSE leg 的腿不再发平仓单（防重复发单）。
	closedTickets := make(map[string]bool)
	for _, l := range legs {
		if l.Phase == "CLOSE" && l.Status == "FILLED" {
			closedTickets[l.OrderID] = true
		}
	}

	var totalPnL float64
	for _, leg := range legs {
		// D-159 W2a R3-2：只对 Phase=OPEN AND Status=FILLED 的开仓腿发平仓单。
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		// D-159 W2a R3-3：已存在 CLOSE leg 的腿跳过（防重复发单）。
		if closedTickets[leg.OrderID] {
			log.Printf("[exec] close leg %d ticket=%s already has CLOSE leg, skip", leg.ID, leg.OrderID)
			continue
		}
		exe, ok := p.registry.Get(leg.Venue)
		if !ok {
			continue
		}
		// D-068 修复：MT5/MT4 用 CloseOrder（按 ticket 平仓）；CEX 用 PlaceOrder 反向减仓。
		var fill Fill
		var closeErr error
		if strings.HasPrefix(leg.Venue, "mt5:") || strings.HasSuffix(leg.Venue, ":mt5") ||
			strings.HasPrefix(leg.Venue, "mt4:") || strings.HasSuffix(leg.Venue, ":mt4") {
			fill, closeErr = exe.CloseOrder(ctx, OrderRef{OrderID: leg.OrderID, Venue: leg.Venue})
		} else {
			// CEX 反向减仓（D-059 posSide 修正）
			// D-121：平仓也走 PlaceOrder → 同样需 Precheck（gate 合约不存在则平仓失败→卡 CLOSING）
			closeSide := "SELL"
			if leg.Side == "SELL" {
				closeSide = "BUY"
			}
			if pc, ok := exe.(Prechecker); ok {
				if pcErr := pc.Precheck(ctx, Order{
					Symbol: leg.Symbol,
					Side:   closeSide,
					Qty:    leg.Qty,
					Venue:  leg.Venue,
				}); pcErr != nil {
					log.Printf("[exec] close leg %d precheck fail (venue=%s symbol=%s): %v", leg.ID, leg.Venue, leg.Symbol, pcErr)
					continue // 合约不存在→跳过此腿（无法平仓，留 CLOSING 等手动处置）
				}
			}
			var positionSide string
			switch leg.Side {
			case "BUY":
				positionSide = "long"
			case "SELL":
				positionSide = "short"
			}
			fill, closeErr = exe.PlaceOrder(ctx, Order{
				Symbol:       leg.Symbol,
				Side:         closeSide,
				Qty:          leg.Qty,
				Venue:        leg.Venue,
				PositionSide: positionSide,
				PosID:        posID, // D-108 P2 T1：平仓被拒 critical 告警 posID 挂接
			})
		}
		if closeErr != nil {
			log.Printf("[exec] close leg %d (ticket=%s venue=%s): %v", leg.ID, leg.OrderID, leg.Venue, closeErr)
			continue
		}
		// D-069：记录 CLOSE phase leg（平仓价/订单ID/方向持久化，供成交历史复盘）
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		// D-159 W2a R3-4：平仓 fill 状态/数量/价格必须有效，禁零价格/零数量伪造成交。
		if fill.Qty <= 0 || fill.Price <= 0 {
			msg := fmt.Sprintf("close leg %d invalid fill: qty=%.4f price=%.4f (skip, not faked)", leg.ID, fill.Qty, fill.Price)
			_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
			log.Printf("[exec] %s", msg)
			continue // 跳过此腿，不伪造成交
		}
		closePrice := fill.Price
		// D-108 P2 T2：close leg Qty 改记 fill.Qty（实际成交量，原记 leg.Qty 全额）。
		// 部分平仓时 fill.Qty < leg.Qty → CLOSE leg 记实际成交量 + A2 残留回读 = 不置 CLOSED 兜底闭环。
		closeQty := fill.Qty
		if closeQty <= 0 {
			closeQty = leg.Qty // fallback：fill 无 qty（MT5 CloseOrder 异常）用原腿 qty 不丢数据
		}
		// D-108 A1 fail-closed：CLOSE phase 腿写库失败 → critical 告警 + 返回 error（拒绝置 CLOSED，对账兜底）
		// D-159 W1a R3-1：ErrTerminalState=该 CLOSE leg 已被对账收尾写入（并发），幂等跳过该腿不报错。
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         leg.LegNo,
			Phase:         "CLOSE",
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
				log.Printf("[exec] close leg %d already persisted (concurrent reconcile), skip", leg.ID)
				continue
			}
			msg := fmt.Sprintf("close leg persist failed position=%d leg=%d venue=%s: %v", posID, leg.LegNo, leg.Venue, err)
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
			return fmt.Errorf("close leg persist: %w", err)
		}
		// D-077 修复⑪A M1：平仓 PnL 权威化——删简化公式，改双路。
		// 同步路径（即刻）：(Fill.Price − 开仓价) × qty × CS × 方向 + Fill.Swap − Fill.Commission
		// CS 经服务器规格换算（修掉漏 CS 的 100 倍错误）；CEX 腿 CS=1（qty 即基础币）。
		// D-105 T2：整腿 × fx（计价币→USD；fx 缺失 → 0 + critical alert，宁缺勿虚增，对账兜底）。
		legPnL := p.calcLegPnL(leg, fill, fx)
		totalPnL += legPnL
		if fxMissing {
			_ = p.store.InsertAlert(ctx, &posID, "critical",
				fmt.Sprintf("pnl fx missing venue=%s symbol=%s quoteCcy=%s", leg.Venue, leg.Symbol, quoteCcy))
		}
		// swap/commission 入 cashflows（D-105 T2：原 quote-ccy 值 × fx 换算 USD，cashflows 语义=USD）
		// D-189 P0-2：统一走 writeCloseCashflows（显式错误处理 + per-leg/day 幂等）；
		// 任一写入失败 → 返回 error 保持 CLOSING，禁止继续到 store.ClosePosition。
		if err := p.writeCloseCashflows(ctx, posID, leg.LegNo, leg.Venue, fill.Swap, fill.Commission, fx); err != nil {
			return fmt.Errorf("pos=%d leg=%d close cashflow: %w", posID, leg.LegNo, err)
		}
	}

	// D-077 修复⑩ G1：平仓后 ticket 回读——按 ticket 回读各 venue 在仓订单，
	// ticket 仍在 → 不置 CLOSED + critical「broker 残留腿」（防 DB 假 CLOSED 留孤儿）。
	// D-108 A2 fail-closed：verifier 为 nil 或查询失败 → 拒绝置 CLOSED + critical 告警 + 返回 error
	// （下轮重试，对账兜底；原 fail-open 放行=假 CLOSED 风险）。
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
		// 不置 CLOSED，留 OPEN 状态等对账自动收尾兜底
		return fmt.Errorf("residual legs after close: %s", strings.Join(residual, ", "))
	}

	// D-077 修复⑪A M1 回读校准：历史单权威 PnL 覆盖同步路径公式。
	// D-159 W3a R4：calibrator 未接线（nil）→ 用同步公式，不标记 estimated（合法配置）。
	// calibrator 接线但部分校准失败（calibratePnL 要求全部应校准腿成功）→ 标记 estimated
	//（settlement 跳过不入正式统计，R4「无法完成校准不进正式策略统计」）。
	if p.pnlCalibrator != nil {
		if authPnL, ok := p.calibratePnL(ctx, legs, fx); ok {
			log.Printf("[exec] position %d PnL calibrated: sync=%.2f → auth=%.2f", posID, totalPnL, authPnL)
			totalPnL = authPnL
		} else {
			// D-159 W3a R4：calibrator 接线但校准失败 → 标记 estimated。
			_ = p.store.SetPositionNote(ctx, posID, "pnl_estimated: partial or no calibration")
		}
	}

	// D-159 W2a R3-10：所有腿实际成交+残量归零才写 CLOSED。
	// 校验：每个 FILLED OPEN 腿必须有对应 CLOSE leg（phase=CLOSE, status=FILLED）。
	// 残量未归零 → 保持 CLOSING + critical 告警，不置 CLOSED。
	closeLegs, err := p.store.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if err != nil {
		return fmt.Errorf("list close legs: %w", err)
	}
	openFilled := 0
	closeFilled := 0
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
		// D-159 W1a R3-1：ErrTerminalState=已 CLOSED（并发对账先收尾），幂等成功不报错。
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already CLOSED (concurrent reconcile claimed close)", posID)
			return nil // 幂等成功
		}
		// D-159 W2a R3-8：store.ClosePosition 失败必须向调用方返回失败（不吞咽）。
		log.Printf("[exec] close position %d store update: %v", posID, err)
		return fmt.Errorf("close position %d store: %w", posID, err)
	}
	return nil
}
