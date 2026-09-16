package dashboard

// broker position 匹配 + 名义价值计算。
// matchBrokerLegs 按 venueKey+symbol+positionId 唯一匹配（D-117 修复 D-095 double-counting）。

import (
	"context"

	"arb/internal/notional"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// filterOpenLegs 取 OPEN phase 腿（匹配 broker position 用）。
func filterOpenLegs(legs []pgstore.PositionLeg) []pgstore.PositionLeg {
	var out []pgstore.PositionLeg
	for _, l := range legs {
		if l.Phase == "OPEN" {
			out = append(out, l)
		}
	}
	return out
}

// matchBrokerLegs 按 OPEN 腿 venueKey+symbol+positionId 匹配 broker position，求 Σ profit / Σ swap / Σ commission。
// 返回 (floating, swapAccrued, commission, matched)；matched=false = 全部腿未匹配。
// D-117：加 positionId 约束（避免同 venue+symbol 跨 position double-counting，D-095 遗留 bug）；
//
//	加 commission 返回值（net = floating + swap + commission，含佣金成本）。
//
// broker position 的 positionId 由 annotatePositionID 标注（(venueKey,ticket)→posID 索引）；
// positionId=0（孤儿订单/未关联）不匹配任何 leg（fail-closed，不冒充）。
func matchBrokerLegs(openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, posID int64) (float64, float64, float64, bool) {
	var floating, swap, commission float64
	matched := false
	for _, leg := range openLegs {
		for _, bp := range brokerSnap {
			if bp.VenueKey == leg.Venue && bp.Symbol == leg.Symbol && bp.PositionId == posID {
				floating += bp.Pnl
				swap += bp.Swap
				commission += bp.Commission
				matched = true
			}
		}
	}
	return floating, swap, commission, matched
}

// computeEntryNotional Σ OPEN FILLED 腿 LegUSD（CS+fx 经 store 查询）。
// 与 settlement.computeNotionalUSD 同构（P3 复用 notional.LegUSD/QuoteCcy/VenueAccount，不复制实现）。
// 缺 CS/fx → 该腿跳过（notional 可能部分算）；全缺 → 返回 0（应得缺省）。
func computeEntryNotional(ctx context.Context, s *Service, openLegs []pgstore.PositionLeg, canonical string) float64 {
	if s.store == nil || canonical == "" {
		return 0
	}
	quoteCcy := notional.QuoteCcy(canonical)
	total := 0.0
	for _, l := range openLegs {
		if l.Status != "FILLED" || l.PriceActual == nil || *l.PriceActual <= 0 || l.Qty <= 0 {
			continue
		}
		cs := 1.0
		if c, ok, _ := s.store.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
			cs = c
		}
		fx := 1.0
		if quoteCcy != "" && quoteCcy != "USD" {
			rate, ok := s.store.USDPer(ctx, quoteCcy, l.Ts)
			if !ok {
				continue
			}
			fx = rate
		}
		total += notional.LegUSD(l.EffectiveQty(), cs, *l.PriceActual, fx) // D-311 R6：成交口径（CEX=基础币）
	}
	return total
}
