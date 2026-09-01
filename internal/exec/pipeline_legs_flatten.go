// pipeline_legs_flatten.go —— D-127 P3b：LEGGED 自动收平（纯 CEX 组合限定）。
// 从 pipeline.go 拆出（pipeline.go >300 行软警告）。
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

// allCEXLegs 判断 legs 是否全部为 CEX 腿（无 MT4/MT5）。
func allCEXLegs(legs []LegSpec) bool {
	for _, l := range legs {
		if isMTVenueKey(l.VenueKey) {
			return false
		}
	}
	return true
}

// autoFlattenLEGGED 对 LEGGED 仓的 FILLED 腿反向 MARKET 平仓（D-127 P3b）。
// 全部收平成功 → true；任一失败 → false（维持 LEGGED，人工兜底）。
// 复用既有 CEX 平仓链路（binance positionSide/reduceOnly D-108 P2、okx posSide D-125）。
// D-159 W2a S4：收平成功后写 CLOSE leg 到 DB，供 ClosePosition 的 closedTickets 检查跳过重复发单。
// D-189 P0-2：写 CLOSE leg 后调 writeCloseCashflows（与 pipeline_legs_close.go 同口径），失败保持未完成。
func (p *Pipeline) autoFlattenLEGGED(ctx context.Context, posID int64, legs []LegSpec, results []LegResult) (bool, error) {
	// D-189 P0-2：计价币→USD 换算（与 ClosePosition 同口径，供现金流写入用）。
	pos, err := p.store.GetPosition(ctx, posID)
	if err != nil {
		return false, fmt.Errorf("get position %d: %w", posID, err)
	}
	quoteCcy := notional.QuoteCcy(canonicalFromOppID(pos.OpportunityID))
	fx := 1.0
	if quoteCcy != "" && quoteCcy != "USD" {
		if p.fxResolver != nil {
			if rate, ok := p.fxResolver.USDPer(ctx, quoteCcy, time.Now()); ok && rate > 0 {
				fx = rate
			} else {
				fx = 0 // fx 缺失 → 0，宁缺勿虚增（对账兜底）
			}
		} else {
			fx = 0
		}
	}
	// D-159 W2a S4：查 DB OPEN legs 获取完整 leg 信息（LegNo/Account 等），供写 CLOSE leg 用。
	dbLegs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		log.Printf("[exec] auto-flatten pos=%d list legs: %v", posID, err)
		return false, err
	}
	legByNo := make(map[int16]pgstore.PositionLeg)
	for _, l := range dbLegs {
		if l.Phase == "OPEN" {
			legByNo[l.LegNo] = l
		}
	}

	allFlattened := true
	for i, r := range results {
		if r.Status != "FILLED" || r.Qty <= 0 {
			continue
		}
		leg := legs[i]
		exe, ok := p.registry.Get(leg.VenueKey)
		if !ok {
			log.Printf("[exec] auto-flatten pos=%d leg=%d: no executor for %s", posID, i, leg.VenueKey)
			allFlattened = false
			continue
		}
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		var positionSide string
		switch leg.Side {
		case "BUY":
			positionSide = "long"
		case "SELL":
			positionSide = "short"
		}
		fill, err := exe.PlaceOrder(ctx, Order{
			Symbol:       leg.VenueSymbol,
			Side:         closeSide,
			Qty:          r.Qty,
			Venue:        leg.VenueKey,
			PositionSide: positionSide,
			PosID:        posID,
		})
		if err != nil {
			log.Printf("[exec] auto-flatten pos=%d leg=%d venue=%s: %v", posID, i, leg.VenueKey, err)
			allFlattened = false
			continue
		}
		log.Printf("[exec] auto-flatten pos=%d leg=%d venue=%s: closed qty=%v price=%v", posID, i, leg.VenueKey, fill.Qty, fill.Price)

		// D-159 W2a S4：写 CLOSE leg 到 DB（供 ClosePosition closedTickets 检查跳过重复发单）。
		dbLeg, ok := legByNo[int16(i)]
		if !ok {
			log.Printf("[exec] auto-flatten pos=%d leg=%d: DB leg not found, skip CLOSE leg write", posID, i)
			continue
		}
		closePrice := fill.Price
		closeQty := fill.Qty
		if closeQty <= 0 {
			closeQty = r.Qty
		}
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         dbLeg.LegNo,
			Phase:         "CLOSE",
			Venue:         dbLeg.Venue,
			Account:       dbLeg.Account,
			Symbol:        dbLeg.Symbol,
			Side:          closeSide,
			Qty:           closeQty,
			PriceExpected: dbLeg.PriceExpected,
			PriceActual:   &closePrice,
			OrderID:       dbLeg.OrderID, // F2 修法：原 OPEN ticket（与 ClosePosition closedTickets 检查对齐，范式同 auto_close.go:73 goneLeg.OrderID）；fill.OrderID 是 CEX 平价新单号会导致查不到→重复发单
			Status:        "FILLED",
		}); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] auto-flatten pos=%d leg=%d write CLOSE leg: %v", posID, i, err)
			allFlattened = false
			continue
		}
		// D-189 P0-2：写 CLOSE leg 后调 writeCloseCashflows（与 pipeline_legs_close.go 同口径）。
		// 失败保持未完成（allFlattened=false），不继续置 CLOSED。
		if err := p.writeCloseCashflows(ctx, posID, dbLeg.LegNo, dbLeg.Venue, fill.Swap, fill.Commission, fx); err != nil {
			log.Printf("[exec] auto-flatten pos=%d leg=%d write cashflow: %v", posID, i, err)
			allFlattened = false
			continue
		}
	}
	return allFlattened, nil
}
