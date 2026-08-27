package dashboard

// D-095 持仓表第一性重构：enrichPositions 对活跃行派生 7 个 optional 字段。
// 设计 SSOT：docs/持仓表第一性重构设计.md §3（零信任——派生字段全部后端）。
//
// 派生口径：
//   - floating_pnl_usd / swap_accrued_usd / net_pnl_usd：bpm.Snapshot() 按 OPEN 腿 venueKey+symbol 匹配 broker position，
//     Σ profit→floating、Σ swap→swap_accrued、和→net（任一腿未匹配即该腿两项不计入，全部未匹配→三字段缺省）；
//   - expected_swap_usd_to_date：expected_swap_bps_daily/10000 × 入场名义 × holdDays（息差型）；
//   - attainment_pct：swap_accrued / expected_swap_usd_to_date × 100（应得≤0 或 swap 缺省→缺省）；
//   - current_net_bps：ListComboDaily(comboKey, 1) 最新 net_bps（收敛型）。
//
// holdDays = now − opened_at（活跃行无 closed_at；与 settlement.computeHoldDays 同构但终点=now 非 closed_at，注释指认不复制）。
// 名义 = Σ notional.LegUSD(qty, CS, PriceActual, fx)，CS=store.ContractSizeByCanonical、fx=store.USDPer（D-087 计算器同构）。

import (
	"context"
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/notional"
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
func (s *Service) enrichPositions(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) {
	if s.bpm == nil {
		return // 测试未接线 → 整组跳过（诚实缺省，不伪造）
	}
	brokerSnap := s.bpm.Snapshot()
	annotatePositionID(ctx, s.store, brokerSnap) // D-117：标注 positionId（与 ListBrokerPositions 同源）
	now := time.Now()
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] {
			continue // CLOSED/FAILED 不 enrich
		}
		legs := legsByPos[pb.Id]
		enrichOne(ctx, s, pb, legs, brokerSnap, now)
	}
}

// enrichOne 单行派生（拆出便于测试）。
func enrichOne(ctx context.Context, s *Service, pb *v1.Position, legs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, now time.Time) {
	// 1. 匹配 broker position：OPEN 腿 venueKey+symbol+positionId → Σ profit / Σ swap / Σ commission
	// D-117 修正：加 positionId 约束，避免同 venue+symbol 跨 position double-counting（D-095 遗留 bug）
	openLegs := filterOpenLegs(legs)
	floating, swapAccrued, commission, matched := matchBrokerLegs(openLegs, brokerSnap, pb.Id)
	if matched {
		pb.FloatingPnlUsd = &floating
		pb.SwapAccruedUsd = &swapAccrued
		net := floating + swapAccrued + commission
		pb.NetPnlUsd = &net

		// D-117：回本周期（实时计算，非理论值）
		// 日均 swap = 已收 swap ÷ 持有天数（实际值）；净值<0 → |净值|÷日均 swap；≥0 → 0（已回本）
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

	// 2. 息差型：应得 + 达成率
	if carryTypes[pb.Type] && pb.ExpectedSwapBpsDaily != nil && *pb.ExpectedSwapBpsDaily > 0 {
		canonical := canonicalFromOppID(pb.OpportunityId)
		notionalUSD := computeEntryNotional(ctx, s, openLegs, canonical)
		if notionalUSD > 0 {
			holdDays := holdDaysToNow(pb, openLegs, now)
			if holdDays > 0 {
				expected := *pb.ExpectedSwapBpsDaily / 10000 * notionalUSD * holdDays
				pb.ExpectedSwapUsdToDate = &expected
				if swapAccrued > 0 && expected > 0 {
					pct := swapAccrued / expected * 100
					pb.AttainmentPct = &pct
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
		total += notional.LegUSD(l.Qty, cs, *l.PriceActual, fx)
	}
	return total
}

// holdDaysToNow (now − opened_at)/24h；opened_at NULL → min(OPEN FILLED 腿 ts)。
// 与 settlement.computeHoldDays 同构（终点=now 非 closed_at；活跃行持有至今）。
func holdDaysToNow(pb *v1.Position, openLegs []pgstore.PositionLeg, now time.Time) float64 {
	var openedAt time.Time
	if pb.OpenedAt != nil {
		openedAt = pb.OpenedAt.AsTime()
	} else {
		var minTs time.Time
		for _, l := range openLegs {
			if l.Status == "FILLED" {
				if minTs.IsZero() || l.Ts.Before(minTs) {
					minTs = l.Ts
				}
			}
		}
		if !minTs.IsZero() {
			openedAt = minTs
		}
	}
	if openedAt.IsZero() {
		return 0
	}
	return now.Sub(openedAt).Hours() / 24
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// 与 settlement/autotrade 同构纯函数（本包不依赖 autotrade 避免环依赖；combo.FromOppID 已引入）。
func canonicalFromOppID(oppID string) string {
	ck := combo.FromOppID(oppID) // 剥离旧 tick 尾时间戳
	parts := splitColon(ck)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// splitColon 按冒号分割（combo.FromOppID 已处理 venue 含 ":" 的边界，此处 ck 已是组合键）。
func splitColon(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// computeCurrentBps 用实时报价重算收敛型当前 bps（D-111）。
// CROSS_VENUE: net_bps = (sell.Bid - buy.Ask) / mid * 10000 - (commissionBps + slippageCost) * 2
// MEANREVERT: bps = spread / ask * 10000（gross，无成本扣除）
// 报价缺失/异常 → ok=false（fail-closed，不回退 rollup）。
// 公式与 detector.tryCross / evaluator.Evaluate 同构（P3 单一真相源——公式简单不值得抽取，注释指认）。
func computeCurrentBps(pb *v1.Position, openLegs []pgstore.PositionLeg, qbus *bus.QuoteBus, commissionFor func(string) float64, slippageBps float64) (float64, bool) {
	canonical := canonicalFromOppID(pb.OpportunityId)
	if canonical == "" || qbus == nil || len(openLegs) < 2 {
		return 0, false
	}
	// 找 BUY 腿和 SELL 腿
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range openLegs {
		if openLegs[i].Phase != "OPEN" {
			continue
		}
		switch openLegs[i].Side {
		case "BUY":
			buyLeg = &openLegs[i]
		case "SELL":
			sellLeg = &openLegs[i]
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	// 取当前报价
	qBuy, okBuy := qbus.Latest(canonical, buyLeg.Venue)
	qSell, okSell := qbus.Latest(canonical, sellLeg.Venue)
	if !okBuy || !okSell {
		return 0, false
	}
	switch pb.Type {
	case "CROSS_VENUE":
		spread := qSell.Bid - qBuy.Ask
		if spread <= 0 {
			return 0, false
		}
		mid := qBuy.Mid()
		if mid <= 0 {
			return 0, false
		}
		grossBps := spread / mid * 10000
		commissionBps := 0.0
		if commissionFor != nil {
			commissionBps = commissionFor(buyLeg.Venue) + commissionFor(sellLeg.Venue)
		}
		slippageCost := slippageBps * 2
		netBps := grossBps - (commissionBps+slippageCost)*2
		return netBps, true
	case "MEANREVERT":
		spread := qSell.Bid - qBuy.Ask
		if spread <= 0 {
			return 0, false
		}
		if qBuy.Ask <= 0 {
			return 0, false
		}
		bps := spread / qBuy.Ask * 10000
		return bps, true
	default:
		return 0, false
	}
}
