package autotrade

import (
	"context"
	"time"

	"arb/internal/spread"
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
	bps, _, ok := e.currentBasisBpsAged(p)
	return bps, ok
}

// currentBasisBpsLegs D-004 P1#4：legs 外给变体（basisStopCheckLegs 用）。
func (e *Engine) currentBasisBpsLegs(p *pgstore.Position, legs []pgstore.PositionLeg) (float64, bool) {
	bps, _, ok := e.currentBasisBpsAgedLegs(p, legs)
	return bps, ok
}

// currentBasisBpsLegsResolved D-004 P1#4：sellCanonical 已解析零 DB 变体（quote 快路）。
func (e *Engine) currentBasisBpsLegsResolved(p *pgstore.Position, legs []pgstore.PositionLeg, sellCanonical string) (float64, bool) {
	bps, _, ok := e.currentBasisBpsAgedResolved(p, legs, sellCanonical)
	return bps, ok
}

// currentBasisBpsAged D-378b S1：带报价龄变体——minQuoteAge=双腿 now−Quote.Time 较大者
// （灾难带新鲜度闸取数；原 currentBasisBps 改一行包装丢 age，既有调用点零签名变更零行为变更）。
func (e *Engine) currentBasisBpsAged(p *pgstore.Position) (float64, time.Duration, bool) {
	if e.quotes == nil {
		return 0, 0, false
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, 0, false
	}
	return e.currentBasisBpsAgedLegs(p, legs)
}

// currentBasisBpsAgedLegs D-004 P1#4：legs 外给变体——卖腿 canonical 经
// xassetSellCanonical 解析（含 DB），调用方=权威路径；quote 快路走
// currentBasisBpsAgedResolved + openEntry.sellCanonical 缓存（零 DB）。
func (e *Engine) currentBasisBpsAgedLegs(p *pgstore.Position, legs []pgstore.PositionLeg) (float64, time.Duration, bool) {
	sellCanonical := e.xassetSellCanonical(context.Background(), p, legs)
	return e.currentBasisBpsAgedResolved(p, legs, sellCanonical)
}

// currentBasisBpsAgedResolved D-004 P1#4：sellCanonical 已解析的零 DB 变体
// （positionLoop openEntry 缓存供给；"" = 解析不出→类型分支兜底/fail-closed 同原语义）。
func (e *Engine) currentBasisBpsAgedResolved(p *pgstore.Position, legs []pgstore.PositionLeg, sellCanonical string) (float64, time.Duration, bool) {
	if e.quotes == nil {
		return 0, 0, false
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, 0, false
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
		return 0, 0, false
	}
	if sellCanonical == "" {
		if p.Type == "XASSET_CARRY" {
			return 0, 0, false // XASSET 跨 canonical：解析不出不拿买 canonical 错查
		}
		sellCanonical = canonical // 旧行为回退（SWAPARB/CARRY 同 canonical 组合）
	}
	buyQuote, ok1 := e.quotes.Latest(canonical, buyLeg.Venue)
	sellQuote, ok2 := e.quotes.Latest(sellCanonical, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	// D-378b S1：报价龄=双腿 now−Quote.Time 较大者（周末/收盘假偏移防误杀）
	minQuoteAge := time.Since(buyQuote.Time)
	if a := time.Since(sellQuote.Time); a > minQuoteAge {
		minQuoteAge = a
	}
	// D-424：basis=清算侧价差（buyBid−sellAsk），PnL_bps=basis−anchor；与收敛判据（sellBid−buyAsk 剩余价差）是两量别混
	spread := buyQuote.Bid - sellQuote.Ask
	refPrice := buyQuote.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	if refPrice <= 0 {
		return 0, 0, false
	}
	return spread / refPrice * 10000, minQuoteAge, true
}

// currentNetBps 实时计算持仓的当前净 bps（收敛型平仓用）。
// D-108：从 engine_position.go 拆出（312→269 行达标）。
func (e *Engine) currentNetBps(p *pgstore.Position) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true
	}
	legs, err := e.store.ListPositionLegs(context.Background(), p.ID)
	if err != nil {
		return 0, false
	}
	return e.currentNetBpsLegs(p, legs)
}

// currentNetBpsLegs D-004 P1#4：legs 外给变体——positionLoop quote 快路复用缓存
// 腿集零 DB；权威路径（本函数上游）仍逐仓现查。
func (e *Engine) currentNetBpsLegs(p *pgstore.Position, legs []pgstore.PositionLeg) (float64, bool) {
	if e.quotes == nil {
		return p.ExpectedNetBps, true
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return p.ExpectedNetBps, true
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
	// D-424：剩余可捕捉价差 = sellBid − buyAsk（与入场口径同源；≤0=已过度收敛→平）
	refPrice := buyClose.Ask
	if buyLeg.PriceActual != nil && *buyLeg.PriceActual > 0 {
		refPrice = *buyLeg.PriceActual
	}
	return spread.RemainingBps(sellClose.Bid, buyClose.Ask, refPrice), true
}
