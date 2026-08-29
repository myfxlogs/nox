package dashboard

// D-095 持仓表第一性重构：enrichPositions 对活跃行派生 7 个 optional 字段。
// 设计 SSOT：docs/持仓表第一性重构设计.md §3（零信任——派生字段全部后端）。
//
// 派生口径：
//   - floating_pnl_usd / swap_accrued_usd / net_pnl_usd：bpm.Snapshot() 按 OPEN 腿 venueKey+symbol+positionId 匹配 broker position，
//     Σ profit→floating、Σ swap→swap_accrued、和→net（任一腿未匹配即该腿两项不计入，全部未匹配→三字段缺省）；
//   - expected_swap_usd_to_date：D-118 结算日历感知——dailySwap × (结算天数 + 2×三倍日数)；
//   - attainment_pct：swap_accrued / expected_swap_usd_to_date × 100（应得≤0 或 swap 缺省→缺省）；
//   - current_net_bps：ListComboDaily(comboKey, 1) 最新 net_bps（收敛型）。
//
// holdDays = now − opened_at（活跃行无 closed_at；与 settlement.computeHoldDays 同构但终点=now 非 closed_at，注释指认不复制）。
// 名义 = Σ notional.LegUSD(qty, CS, PriceActual, fx)，CS=store.ContractSizeByCanonical、fx=store.USDPer（D-087 计算器同构）。
//
// 拆分文件：_calendar.go（D-118 结算日历）+ _match.go（broker 匹配+名义）+ _helpers.go（持有天数+canonical+bps）。

import (
	"context"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// activeStatuses 活跃态集合（enrich 仅对这些行计算；CLOSED/FAILED 只填 realized 既有）。
var activeStatuses = map[string]bool{
	"OPENING": true, "OPEN": true, "LEGGED": true, "CLOSING": true,
}

// carryTypes 息差型（填 12/16/17）；收敛型填 18，互不越界。
var carryTypes = map[string]bool{
	"SWAPARB": true, "CARRY": true, "FUNDING_HEDGE": true, "PERP_FUNDING": true,
}

// convergentTypes 收敛型（填 current_net_bps）。
var convergentTypes = map[string]bool{
	"CROSS_VENUE": true, "MEANREVERT": true,
}

// enrichPositions 对活跃行计算 7 派生字段，原地修改 pb positions。
// bpm 为 nil（测试未接线）时整组跳过；legs 按 positionID 批量预取。
// D-117：brokerSnap 需标注 positionId（与 ListBrokerPositions 同源），matchBrokerLegs 按 positionId 唯一匹配。
// D-118：批量预取 per-venue triple_swap_day（facts 表），传给 enrichOne 算结算日历感知预期 swap。
// D-146 S5：批量预取 per-position funding 实收（position_cashflows），传给 enrichOne 扩展回本口径。
func (s *Service) enrichPositions(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) {
	if s.bpm == nil {
		return // 测试未接线 → 整组跳过（诚实缺省，不伪造）
	}
	brokerSnap := s.bpm.Snapshot()
	annotatePositionID(ctx, s.store, brokerSnap) // D-117：标注 positionId（与 ListBrokerPositions 同源）
	// D-118：批量预取 per-venue triple_swap_day（从 OPEN legs 提取 venue 列表）
	tripleDayByVenue := s.prefetchTripleSwapDays(ctx, pbPositions, legsByPos)
	// D-146 S5：批量预取 per-position funding 实收（CEX 仓回本口径扩展）
	fundingByPos := s.prefetchFundingCashflows(ctx, pbPositions)
	now := time.Now()
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] {
			continue // CLOSED/FAILED 不 enrich
		}
		legs := legsByPos[pb.Id]
		enrichOne(ctx, s, pb, legs, brokerSnap, now, tripleDayByVenue, fundingByPos)
	}
}

// prefetchFundingCashflows D-146 S5：批量预取活跃持仓的 funding 实收（position_cashflows SUM WHERE kind='funding'）。
// store=nil（测试未接线）→ 返回空 map（enrichOne 无 funding 加成，纯 MT 仓行为不变）。
// 查询失败 → 该 position 不入 map（enrichOne 视为 funding=0，fail-closed 不冒充）。
func (s *Service) prefetchFundingCashflows(ctx context.Context, pbPositions []*v1.Position) map[int64]float64 {
	out := make(map[int64]float64)
	if s.store == nil {
		return out // 测试未接线 → 空 map（enrichOne 无 funding 加成）
	}
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] {
			continue
		}
		sum, err := s.store.SumFundingCashflows(ctx, pb.Id)
		if err == nil && sum != 0 {
			out[pb.Id] = sum
		}
		// 查询失败/sum=0 → 不入 map（enrichOne 视为 funding=0）
	}
	return out
}

// enrichOne 单行派生（拆出便于测试）。
// D-118：tripleDayByVenue 为 per-venue triple_swap_day 预取 map（可能空，enrichOne 退化为纯结算天数）。
// D-146 S5：fundingByPos 为 per-position funding 实收预取 map（可能空，enrichOne 无 funding 加成）。
//
// 回本口径扩展（D-146 S5）：
//   - swapAccrued = broker 快照 Σ swap + position_cashflows funding 实收（CEX 仓 PERP_FUNDING 并入）
//   - net = floating + commission + swapAccrued（同口径：floating+commission+swap+funding）
//   - matched=false 但 funding>0 → 仍出值（CEX 仓有 funding 流→回本显示数值）
//   - matched=false 且 funding=0 → 三字段缺省（零流仓仍「—」）
func enrichOne(ctx context.Context, s *Service, pb *v1.Position, legs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, now time.Time, tripleDayByVenue map[string]time.Weekday, fundingByPos map[int64]float64) {
	// 1. 匹配 broker position：OPEN 腿 venueKey+symbol+positionId → Σ profit / Σ swap / Σ commission
	// D-117 修正：加 positionId 约束，避免同 venue+symbol 跨 position double-counting（D-095 遗留 bug）
	openLegs := filterOpenLegs(legs)
	floating, swapAccrued, commission, matched := matchBrokerLegs(openLegs, brokerSnap, pb.Id)
	// D-146 S5：funding 实收并入 swapAccrued 口径（CEX 仓 PERP_FUNDING 回本显示）
	funding := fundingByPos[pb.Id] // 不在 map → 0（纯 MT 仓无 funding，行为不变）
	swapAccrued += funding         // 口径扩展：swap 快照 + funding 实收
	if matched || funding != 0 {
		pb.FloatingPnlUsd = &floating
		pb.SwapAccruedUsd = &swapAccrued
		net := floating + swapAccrued + commission
		pb.NetPnlUsd = &net

		// D-117：回本周期（实时计算，非理论值）
		// 日均收益 = (swap 快照 + funding 实收) ÷ 持有天数；净值<0 → |净值|÷日均收益；≥0 → 0（已回本）
		if carryTypes[pb.Type] {
			holdDays := holdDaysToNow(pb, openLegs, now)
			if holdDays > 0 {
				dailySwap := swapAccrued / holdDays
				if dailySwap > 0 {
					var breakEven float64
					if net < 0 {
						breakEven = -net / dailySwap
					}
					// net ≥ 0 → breakEven=0（已回本）；dailySwap>0 保证除法安全
					pb.BreakEvenDays = &breakEven
				}
				// dailySwap ≤ 0 → 缺省（付息型或不结算，不显示回本周期）
			}
		}
	}

	// 2. 息差型：应得 + 达成率（D-118 结算日历感知）
	if carryTypes[pb.Type] && pb.ExpectedSwapBpsDaily != nil && *pb.ExpectedSwapBpsDaily > 0 {
		canonical := canonicalFromOppID(pb.OpportunityId)
		notionalUSD := computeEntryNotional(ctx, s, openLegs, canonical)
		if notionalUSD > 0 {
			openedAt := openedAtTime(pb, openLegs)
			if !openedAt.IsZero() && now.After(openedAt) {
				// D-118：结算日历感知——取双腿 venue 的三倍日（任一腿有三倍日即用）
				tripleDay, hasTriple := pickTripleDay(openLegs, tripleDayByVenue)
				settleDays, tripleDays := settlementDays(openedAt, now, tripleDay, hasTriple)
				if settleDays > 0 {
					// 预期 = dailySwap × (结算天数 + 2×三倍日数)
					// 三倍日 = 3倍 = 1倍基础（已在结算天数里）+ 2倍额外
					dailySwap := *pb.ExpectedSwapBpsDaily / 10000 * notionalUSD
					expected := dailySwap * float64(settleDays+2*tripleDays)
					pb.ExpectedSwapUsdToDate = &expected
					if swapAccrued > 0 && expected > 0 {
						pct := swapAccrued / expected * 100
						pb.AttainmentPct = &pct
					}
				}
			}
		}
	}

	// 3. 收敛型：当前 net bps（实时报价重算，D-111）
	if convergentTypes[pb.Type] {
		if bps, ok := computeCurrentBps(pb, openLegs, s.qbus, s.commissionFor, s.slippageBps); ok {
			pb.CurrentNetBps = &bps
		}
	}
}
