package autotrade

import (
	"context"

	"arb/internal/store/pgstore"
)

// currentBasisBps 重算 SWAPARB/CARRY/XASSET_CARRY 当前基差（两腿 venue 最新报价 cross-broker spread bps）。
// 复用 recalcNetSwap 的 legs/venue 解析链；报价取 e.quotes.Latest。
// 报价缺失 → (0, false) 不决策（与 D-105 fail-closed 一致）。
// D-108 A5：从 engine_position.go 拆出（357→310 行达标）。
// D-311 S5：双腿各自 canonical——买=canonicalFromOppID、卖=canonical_sell（xassetSellCanonical
// 同解析路径）。非 XASSET 类型 canonical_sell 恒 NULL → map 兜底命中同 canonical = 旧行为；
// XASSET 解析不出 → fail-closed（不拿买 canonical 错查卖腿报价——同所跨 canonical 会静默错腿）。
func (e *Engine) currentBasisBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return 0, false
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyLeg = &legs[i]
			} else {
				sellLeg = &legs[i]
			}
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	sellCanonical := e.xassetSellCanonical(context.Background(), p, legs)
	if sellCanonical == "" {
		if p.Type == "XASSET_CARRY" {
			return 0, false // XASSET 跨 canonical：解析不出不拿买 canonical 错查
		}
		sellCanonical = canonical // 旧行为回退（SWAPARB/CARRY 同 canonical 组合）
	}
	buyQuote, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellQuote, ok2 := e.quotes.Latest(sellCanonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false
	}
	// 基差 = 买入腿 bid − 卖出腿 ask（收敛收益的镜像：spread 越大=基差越大=亏损扩大）
	spread := buyQuote.Bid - sellQuote.Ask
	refPrice := buyQuote.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	if refPrice <= 0 {
		return 0, false
	}
	return spread / refPrice * 10000, true
}

// currentNetBps 实时计算持仓的当前净 bps（收敛型平仓用）。
// D-108：从 engine_position.go 拆出（312→269 行达标）。
func (e *Engine) currentNetBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return p.ExpectedNetBps, true
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyLeg = &legs[i]
			} else {
				sellLeg = &legs[i]
			}
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	buyClose, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellClose, ok2 := e.quotes.Latest(canonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false
	}
	spread := buyClose.Bid - sellClose.Ask
	if spread <= 0 {
		return 0, true
	}
	refPrice := buyClose.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	return spread / refPrice * 10000, true
}
