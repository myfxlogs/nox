// pipeline_resume_helpers.go —— D-189 P0-4：ResumeClose 辅助函数（broker preflight / 下单 / 历史回填）。
// 从 pipeline_resume.go 拆出（>300 行软警告）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/store/pgstore"
)

// brokerPreflight D-189 P0-4 S4-2：查 verifier 确认腿是否仍在仓。
// 返回 stillOpen=true 表示 broker 仍有该腿（需发平仓单）；stillOpen=false 表示 broker 已无腿（需历史回填）。
// fill 非零表示已有成交回执（CEX CLOSE/FILLED 权威例外，D-120）；verifier 未接线或查询失败 → fail-closed。
func (p *Pipeline) brokerPreflight(ctx context.Context, posID int64, leg pgstore.PositionLeg) (bool, Fill, error) {
	if p.verifier == nil {
		return false, Fill{}, fmt.Errorf("verifier not wired")
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	orders, err := p.verifier.QueryOpenedOrders(vctx, leg.Venue)
	if err != nil {
		// CEX venue 无 verifier querier（D-120：CEX 平仓 FILLED=权威）。
		if !isMTVenueKey(leg.Venue) {
			return true, Fill{}, nil // CEX 腿：假定仍在仓，走正常平仓路径
		}
		return false, Fill{}, fmt.Errorf("verify %s: %w", leg.Venue, err)
	}
	// MT 腿按 VenueKey+原 OrderID 匹配；CEX 腿按 VenueKey+Symbol+Side+PosSide 匹配（D-195-C2 §6.3 加 position_side）。
	for _, o := range orders {
		if isMTVenueKey(leg.Venue) {
			if o.Ticket == leg.OrderID {
				return true, Fill{}, nil
			}
		} else {
			if o.Symbol == leg.Symbol && o.Side == leg.Side {
				// D-195-C2 §6.3：CEX 聚合仓 position_side 维度匹配。
				// OKX long_short_mode 下 long/short 分列，PosSide 必须匹配才确认是同一持仓。
				// Gate/Binance 从 size 正负推导 PosSide（long/short），与 leg.Side 推导的 expectedPosSide 一致。
				// o.PosSide 为空（旧适配器或 MT 透传）→ 回退到 Symbol+Side 匹配（向后兼容）。
				if o.PosSide == "" {
					return true, Fill{}, nil
				}
				expectedPosSide := "long"
				if leg.Side == "SELL" {
					expectedPosSide = "short"
				}
				if o.PosSide == expectedPosSide {
					return true, Fill{}, nil
				}
			}
		}
	}
	return false, Fill{}, nil // broker 已无该腿 → 历史回填路径
}

// placeCloseOrder D-189 P0-4：发平仓单（MT CloseOrder / CEX PlaceOrder 反向）。
// D-195-C2 §6.4：qty 参数支持部分成交残量继续（全量时 = leg.EffectiveQty()，R6 成交口径）。
func (p *Pipeline) placeCloseOrder(ctx context.Context, posID int64, leg pgstore.PositionLeg, closeSide string, qty float64) (Fill, error) {
	exe, ok := p.registry.Get(leg.Venue)
	if !ok {
		return Fill{}, fmt.Errorf("no executor for venue %s", leg.Venue)
	}
	if isMTVenueKey(leg.Venue) {
		return exe.CloseOrder(ctx, OrderRef{OrderID: leg.OrderID, Venue: leg.Venue})
	}
	if pc, ok := exe.(Prechecker); ok {
		if pcErr := pc.Precheck(ctx, Order{
			Symbol: leg.Symbol, Side: closeSide, Qty: qty, Venue: leg.Venue,
		}); pcErr != nil {
			return Fill{}, fmt.Errorf("precheck: %w", pcErr)
		}
	}
	var positionSide string
	switch leg.Side {
	case "BUY":
		positionSide = "long"
	case "SELL":
		positionSide = "short"
	}
	return exe.PlaceOrder(ctx, Order{
		Symbol: leg.Symbol, Side: closeSide, Qty: qty, Venue: leg.Venue,
		PositionSide: positionSide, PosID: posID,
	})
}

// backfillCloseFromHistory D-189 P0-4 S4-4：broker 已无腿但 CLOSE leg 缺失 → 从权威历史回填。
// pnlCalibrator 命中且 ClosePrice > 0 → 回填 CLOSE leg + PnL + cashflows；无法取得 → fail-closed。
func (p *Pipeline) backfillCloseFromHistory(ctx context.Context, posID int64, leg pgstore.PositionLeg, fx float64) error {
	if p.pnlCalibrator == nil {
		msg := fmt.Sprintf("broker 已无腿但无历史查证器 position=%d leg=%d venue=%s (fail-closed)", posID, leg.LegNo, leg.Venue)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		return fmt.Errorf("no pnl calibrator for backfill")
	}
	hp, err := p.pnlCalibrator.QueryHistoryPnL(ctx, leg.Venue, leg.OrderID, 3)
	if err != nil || hp == nil || hp.ClosePrice <= 0 {
		msg := fmt.Sprintf("broker 已无腿且历史查证无命中 position=%d leg=%d venue=%s ticket=%s (fail-closed)", posID, leg.LegNo, leg.Venue, leg.OrderID)
		_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
		return fmt.Errorf("no authoritative history for backfill")
	}
	closeSide := "SELL"
	if leg.Side == "SELL" {
		closeSide = "BUY"
	}
	closePrice := hp.ClosePrice
	if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: leg.LegNo, Phase: "CLOSE", AttemptNo: 1, Venue: leg.Venue, Account: leg.Account,
		Symbol: leg.Symbol, Side: closeSide, Qty: leg.EffectiveQty(), PriceExpected: leg.PriceExpected, // D-311 R6：回填成交量口径
		PriceActual: &closePrice, OrderID: leg.OrderID, Status: "FILLED",
	}); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return fmt.Errorf("backfill CLOSE leg: %w", err)
	}
	if err := p.writeCloseCashflows(ctx, posID, leg.LegNo, leg.Venue, hp.Swap, hp.Commission, fx); err != nil {
		return fmt.Errorf("backfill cashflow: %w", err)
	}
	return nil
}

// backfillMissingCashflows D-189 P0-4 S4-5：已有 CLOSE leg 但 cashflows 缺失 → 从权威历史补回。
func (p *Pipeline) backfillMissingCashflows(ctx context.Context, posID int64, leg pgstore.PositionLeg, fx float64) error {
	if p.pnlCalibrator == nil {
		return nil
	}
	hp, err := p.pnlCalibrator.QueryHistoryPnL(ctx, leg.Venue, leg.OrderID, 3)
	if err != nil || hp == nil {
		return nil
	}
	if hp.Swap == 0 && hp.Commission == 0 {
		return nil
	}
	return p.writeCloseCashflows(ctx, posID, leg.LegNo, leg.Venue, hp.Swap, hp.Commission, fx)
}
