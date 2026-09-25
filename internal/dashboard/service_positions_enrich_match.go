package dashboard

// broker position 匹配 + 名义价值计算。
// matchBrokerLegs 按 venueKey+symbol+positionId 唯一匹配（D-117 修复 D-095 double-counting）。

import (
	"context"
	"fmt"

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

// fundingForLegs D-322 S3b：CEX 腿 funding 按 venue 去重并入回本口径。
// 去重谓词与 matchBrokerLegs 同型（venueKey+symbol+positionId）：该腿匹配到 broker position
// 且 bp.Swap≠0（venue 实报收讫，已计入 swapAccrued）→ 跳过该 venue；
// 未匹配或 bp.Swap=0（binance/okx 无 Swap 字段恒 0）→ 兜底计入。
// D-375 S2：兜底源由 cashflow 估算换 income_events 实收（estimated cashflow 退出已收列；
// collector 插入期已挂 position_id/leg_no，展示侧不重做匹配）。
// MT 腿不入账（swap 走 broker 快照，口径同 D-146 S5 前）；每 venue 只计一次（income 按
// (pos,venue) 聚合，多腿同 venue 会重复累加）。sums=SumIncomeByPosVenue 预取（key=posID\x00venue）。
func fundingForLegs(posID int64, openLegs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, sums map[string]float64) float64 {
	total := 0.0
	counted := make(map[string]bool)
	for _, leg := range openLegs {
		if !isCEXVenueKey(leg.Venue) || counted[leg.Venue] {
			continue
		}
		counted[leg.Venue] = true
		venueReported := false
		for _, bp := range brokerSnap {
			if bp.VenueKey == leg.Venue && bp.Symbol == leg.Symbol && bp.PositionId == posID && bp.Swap != 0 {
				venueReported = true
				break
			}
		}
		if venueReported {
			continue // venue 真值已在 swapAccrued（matchBrokerLegs Σ swap），估算不双计
		}
		total += sums[fmt.Sprintf("%d\x00%s", posID, leg.Venue)]
	}
	return total
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
		total += s.legNotionalUSD(ctx, l, canonical, quoteCcy)
	}
	return total
}

// legNotionalUSD 单腿名义 USD（computeEntryNotional 的逐腿因子，D-391 拆出供 fee 估算回退复用）。
// 缺 CS → cs=1.0；非 USD 计价缺 fx → 该腿计 0。
func (s *Service) legNotionalUSD(ctx context.Context, l pgstore.PositionLeg, canonical, quoteCcy string) float64 {
	if l.PriceActual == nil || *l.PriceActual <= 0 {
		return 0
	}
	return s.legUSDAt(ctx, l, canonical, quoteCcy, *l.PriceActual)
}

// legUSDAt 单腿按指定价折 USD（D-480 出口价浮动复用同口径——cs/fx 解析同 legNotionalUSD）。
func (s *Service) legUSDAt(ctx context.Context, l pgstore.PositionLeg, canonical, quoteCcy string, px float64) float64 {
	if l.Status != "FILLED" || px <= 0 || l.Qty <= 0 {
		return 0
	}
	cs := 1.0
	if s.oppContractSize != nil { // main 接线=store.ContractSizeByCanonical 同源；nil 回落 store 直查
		if c, ok := s.oppContractSize(canonical, l.Venue); ok && c > 0 {
			cs = c
		}
	} else if s.store != nil {
		if c, ok, _ := s.store.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
			cs = c
		}
	}
	fx := 1.0
	if quoteCcy != "" && quoteCcy != "USD" {
		if s.store == nil {
			return 0
		}
		rate, ok := s.store.USDPer(ctx, quoteCcy, l.Ts)
		if !ok {
			return 0
		}
		fx = rate
	}
	return notional.LegUSD(l.EffectiveQty(), cs, px, fx) // D-311 R6：成交口径（CEX=基础币）
}
