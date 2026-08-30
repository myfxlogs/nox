package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// isContextTimeoutErr D-159 W1a R2：判断 err 是否为 context 超时/取消（MT OrderSend 超时→UNKNOWN 用）。
// 包含 context.DeadlineExceeded、context.Canceled 及 gRPC 层包装的 deadline exceeded。
func isContextTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// gRPC DEADLINE_EXCEEDED 常以字符串包装（rpc error: code = DeadlineExceeded）。
	s := err.Error()
	return strings.Contains(s, "DeadlineExceeded") || strings.Contains(s, "context deadline exceeded")
}

func (p *Pipeline) executeLeg(ctx context.Context, leg LegSpec) LegResult {
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-146 S1：MT 腿下单即带 broker 端灾难 SL（原子写入，断连时服务器端兜底）。
	// CEX 腿不挂（CEX 端有仓位生存期+fail-closed）；仅 :mt4/:mt5 腿且配置 >0 时计算。
	var sl float64
	if isMTVenueKey(leg.VenueKey) {
		sl = disasterSL(leg.Side, leg.PriceExpected, p.mtDisasterSlPct)
	}
	fill, err := exe.PlaceOrder(ctx, Order{
		Symbol:   leg.VenueSymbol,
		Side:     leg.Side,
		Qty:      leg.Qty,
		Venue:    leg.VenueKey,
		StopLoss: sl,
	})
	if err != nil {
		// D-159 W1a R2：MT 腿 OrderSend 超时/响应丢失 → UNKNOWN（不知成交与否，不能假 FAILED 平仓）。
		// context.DeadlineExceeded/Canceled = 网络超时/取消，订单可能已成交 → 标 UNKNOWN 待人工/对账确认。
		// 其他错误（合约不存在/被拒/零成交）= 确定未成交 → FAILED。
		if isMTVenueKey(leg.VenueKey) && isContextTimeoutErr(err) {
			log.Printf("[exec] leg %s %s %s: MT timeout → UNKNOWN (order may have filled): %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
			return LegResult{VenueKey: leg.VenueKey, Status: "UNKNOWN"}
		}
		log.Printf("[exec] leg %s %s %s: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-108 P2 T2：零成交/被拒 → 腿标 FAILED（不再假 FILLED 幻影腿）。
	// MT5 OrderSend 成功但 lots=0/price=0 = 零成交；CEX 已在 PlaceOrder 内报错返回 err。
	if fill.Qty <= 0 || fill.Price <= 0 {
		log.Printf("[exec] leg %s %s %s: zero fill (qty=%v price=%v) → FAILED", leg.VenueKey, leg.Side, leg.VenueSymbol, fill.Qty, fill.Price)
		return LegResult{VenueKey: leg.VenueKey, OrderID: fill.OrderID, Status: "FAILED"}
	}
	return LegResult{
		VenueKey:   leg.VenueKey,
		OrderID:    fill.OrderID,
		Price:      fill.Price,
		Qty:        fill.Qty,
		Status:     "FILLED",
		Swap:       fill.Swap,
		Commission: fill.Commission,
	}
}

// ClosePosition 平仓（§13.3）。
// D-068 修复：MT5 hedging 模式下 PlaceOrder 反向下单 = 开新仓不是平仓。
// MT5/MT4 腿用 CloseOrder（OrderClose 按 ticket 平仓）；CEX 腿用 PlaceOrder 反向减仓（D-059）。
func (p *Pipeline) ClosePosition(ctx context.Context, posID int64) error {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}

	if err := p.store.UpdatePositionStatus(ctx, posID, "CLOSING"); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip CLOSING (G4)", posID)
			return err
		}
		log.Printf("[exec] position %d CLOSING update: %v", posID, err)
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

	var totalPnL float64
	for _, leg := range legs {
		if leg.Status != "FILLED" {
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
		if fill.Swap != 0 {
			_ = p.store.InsertCashflow(ctx, posID, "swap", leg.Venue, fill.Swap*fx, time.Now())
		}
		// D-069：commission 入 cashflows（负号=成本）
		if fill.Commission != 0 {
			_ = p.store.InsertCashflow(ctx, posID, "commission", leg.Venue, -fill.Commission*fx, time.Now())
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
	// 命中 → 用权威值（Profit+Swap−Commission，与 auto_close.go:99 统一）；未命中 → 用同步公式。
	// D-105 T2：权威值同样 × fx（同一 canonical/fxResolver，计价币→USD）。
	if authPnL, ok := p.calibratePnL(ctx, legs, fx); ok {
		log.Printf("[exec] position %d PnL calibrated: sync=%.2f → auth=%.2f", posID, totalPnL, authPnL)
		totalPnL = authPnL
	}

	if err := p.store.ClosePosition(ctx, posID, totalPnL); err != nil {
		// D-159 W1a R3-1：ErrTerminalState=已 CLOSED（并发对账先收尾），幂等成功不报错。
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already CLOSED (concurrent reconcile claimed close)", posID)
		} else {
			log.Printf("[exec] close position %d store update: %v", posID, err)
		}
	}
	return nil
}

// Revalidate 用 QuoteBus 最新报价重算毛收益劣化（§13.1 双阈值；D-051 修③：毛口径比较）。
// 成本侧（佣金/滑点）执行前后不变，净收益劣化完全由毛位移驱动 → 用 originalGrossBps 与重算
// currentBps（spread/mid×10000）同口径比较才正确。返回 true 通过复核，false 劣化超阈值应拒绝。
// D-068 修复：FUNDING_HEDGE 收益来自 funding rate 差不是价格差，价格差近 0 是正常的，
// 用价格差复核会误拒 → FUNDING_HEDGE 只检查报价新鲜度（QuoteBus 有报价即可），跳过价差劣化。
func (p *Pipeline) Revalidate(canonical, venueBuy, venueSell, oppType string, originalGrossBps float64) bool {
	qbuy, ok1 := p.qbus.Latest(canonical, venueBuy)
	qsell, ok2 := p.qbus.Latest(canonical, venueSell)
	if !ok1 || !ok2 {
		return false // 报价缺失 = 不通过
	}
	// FUNDING_HEDGE / PERP_FUNDING：只检查报价新鲜度，跳过价格差劣化
	//（funding rate 差是收益来源，价格差近 0 正常，D-068 修复 FUNDING_HEDGE，
	// D-120 扩展 PERP_FUNDING 同理——gate futures 只有 mark_price 无真实 spread）
	if oppType == "FUNDING_HEDGE" || oppType == "PERP_FUNDING" {
		return true
	}
	// CROSS_VENUE：重算瞬时毛 spread
	spread := qsell.Bid - qbuy.Ask
	mid := qbuy.Mid()
	if mid <= 0 {
		return false
	}
	currentBps := spread / mid * 10000
	// 双阈值：max(absBps 绝对, relPct% 相对)（D-077 修复⑪B N4：env 化 ARB_REVALIDATE_ABS_BPS/ARB_REVALIDATE_REL_PCT）
	absThreshold := p.revalidateAbsBps
	relThreshold := math.Abs(originalGrossBps) * p.revalidateRelPct / 100.0
	threshold := math.Max(absThreshold, relThreshold)
	degradation := originalGrossBps - currentBps
	return degradation <= threshold
}
