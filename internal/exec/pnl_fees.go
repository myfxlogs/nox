// pnl_fees.go —— D-599 S2：净额核算的腿级费用聚合 + fee_quality 定级。
// 口径：FILLED 腿逐条归集——fee_usd 非空=实采（actual）；NULL→按
// feeBpsResolver(venue)×腿名义额估算（estimated）；无估算率=unknown。
// quality 四级：全实采 actual / 全估算 estimated / 实采+估算+未知任一混合 mixed /
// 无 FILLED 腿或零费用信息 none。fees_usd 只载 actual+estimated 合计；
// 拿不到任何费=quality none + fees NULL（不填 0 冒充无费，D-391 惯例延伸）。
package exec

import (
	"arb/internal/store/pgstore"
)

// FeeBpsResolver venue→单腿 taker 佣金率 bps（=commissionResolver 链：
// broker_accounts.commission_bps→calib cfd_fee_bps/cex_taker_fee_bps→配置常量）。
// nil = 估算不可用（NULL 费腿→unknown 而非冒估）。
type FeeBpsResolver func(venueKey string) float64

// SetFeeBpsResolver 注入估算费率链（D-599 S2；main 装配 commissionResolver）。
func (p *Pipeline) SetFeeBpsResolver(r FeeBpsResolver) { p.feeBps = r }

// legNotionalUSD 腿名义额 USD 估算（fee 估算的基数）：
// qty=EffectiveQty（CEX=基础币 MT=手数），price=AvgFillPrice→PriceActual 回落，
// cs=MT 腿合约规格（csResolver 同 legPricePnL 口径；CEX cs=1），fx 计价币→USD。
func (p *Pipeline) legNotionalUSD(leg pgstore.PositionLeg, fx float64) float64 {
	qty := leg.EffectiveQty()
	px := 0.0
	if leg.AvgFillPrice != nil && *leg.AvgFillPrice > 0 {
		px = *leg.AvgFillPrice
	} else if leg.PriceActual != nil && *leg.PriceActual > 0 {
		px = *leg.PriceActual
	}
	if qty <= 0 || px <= 0 || fx <= 0 {
		return 0
	}
	cs := 1.0
	if p.csResolver != nil && isMTVenueKey(leg.Venue) {
		if v, ok := p.csResolver.ContractSize(leg.Venue, leg.Symbol); ok && v > 0 {
			cs = v
		}
	}
	return qty * px * cs * fx
}

// positionFeesUSD 聚合全腿费用 → (feesUSD *float64, feeQuality)。
// legs=OPEN 腿集 + closeLegs=CLOSE 腿集（ResumeClose 调用点已按 phase 分组传入）。
// feesUSD=nil 当且仅当 quality=='none'（未拿到任何费信息）。
func (p *Pipeline) positionFeesUSD(openLegs, closeLegs []pgstore.PositionLeg, fx float64) (*float64, string) {
	var actualSum, estSum float64
	nActual, nEst, nUnk, nFilled := 0, 0, 0, 0
	collect := func(l pgstore.PositionLeg) {
		if l.Status != "FILLED" {
			return
		}
		nFilled++
		if l.FeeUSD != nil {
			actualSum += *l.FeeUSD
			nActual++
			return
		}
		bps := 0.0
		if p.feeBps != nil {
			bps = p.feeBps(l.Venue)
		}
		if notional := p.legNotionalUSD(l, fx); bps > 0 && notional > 0 {
			estSum += notional * bps / 1e4
			nEst++
			return
		}
		nUnk++
	}
	for _, l := range openLegs {
		if l.Phase == "OPEN" {
			collect(l)
		}
	}
	for _, l := range closeLegs {
		collect(l)
	}
	total := actualSum + estSum
	switch {
	case nFilled == 0 || (nActual == 0 && nEst == 0):
		return nil, "none" // 全 FAILED 腿或零费用信息
	case nUnk > 0 || (nActual > 0 && nEst > 0):
		return &total, "mixed" // 实采+估算+未知任一混合
	case nEst > 0:
		return &total, "estimated"
	default:
		return &total, "actual"
	}
}
