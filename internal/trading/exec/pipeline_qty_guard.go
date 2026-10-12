// pipeline_qty_guard.go —— D-026 ②：腿量匹配守卫（FILLED 腿有效量 canonical 口径两两比对）。
// 容差=D-026「venue 步长容差」的执行口径：venue 步长折 canonical 后相对值远小于 1e-4
// （okx XAU 0.001oz/$4175≈2.4e-7），而真实部分成交（IOC/撤单部分量）≥1% 量级——
// 相对容差 1e-4 恰好分隔两者。
package exec

// legQtyMismatch 全 FILLED 腿有效量（canonical 单位）两两偏差 > 相对容差 → true。
// canonical 换算：CEX :perp 腿 LegResult.Qty 已是 base（executeLeg 经 orderQtyFor ×CS）；
// :spot 腿=base 原样；MT 腿=lots → ×CS（csResolver；未接线/取不到=不比对 fail-open——
// 守卫是兜底网，非开仓前置闸）。
func (p *Pipeline) legQtyMismatch(legs []LegSpec, results []LegResult) bool {
	const relTol = 1e-4
	var canon []float64
	for i, r := range results {
		if r.Status != "FILLED" || r.Qty <= 0 {
			continue
		}
		q := r.Qty
		if isMTVenueKey(legs[i].VenueKey) {
			// MT 腿 CS 取不到 → 跳过该腿（裸 lots 与 CEX base 量纲不可比，
			// 拿去两两比对会误判不匹配；fail-open=守卫是兜底网非开仓前置闸）。
			if p.csResolver == nil {
				continue
			}
			cs, ok := p.csResolver.ContractSize(legs[i].VenueKey, legs[i].VenueSymbol)
			if !ok || cs <= 0 {
				continue
			}
			q = r.Qty * cs
		}
		canon = append(canon, q)
	}
	if len(canon) < 2 {
		return false
	}
	lo, hi := canon[0], canon[0]
	for _, q := range canon[1:] {
		if q < lo {
			lo = q
		}
		if q > hi {
			hi = q
		}
	}
	if lo <= 0 {
		return true // 零量腿=确定未有效成交，与正量腿并存即不匹配
	}
	return (hi-lo)/lo > relTol
}
