// pipeline_legs_flatten.go —— D-127 P3b：LEGGED 自动收平（纯 CEX 组合限定）。
// 从 pipeline.go 拆出（pipeline.go >300 行软警告）。
package exec

import (
	"context"
	"log"
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
func (p *Pipeline) autoFlattenLEGGED(ctx context.Context, posID int64, legs []LegSpec, results []LegResult) (bool, error) {
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
	}
	return allFlattened, nil
}
