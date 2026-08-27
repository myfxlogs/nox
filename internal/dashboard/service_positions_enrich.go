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

import (
	"context"
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/fact"
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
// D-118：批量预取 per-venue triple_swap_day（facts 表），传给 enrichOne 算结算日历感知预期 swap。
func (s *Service) enrichPositions(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) {
	if s.bpm == nil {
		return // 测试未接线 → 整组跳过（诚实缺省，不伪造）
	}
	brokerSnap := s.bpm.Snapshot()
	annotatePositionID(ctx, s.store, brokerSnap) // D-117：标注 positionId（与 ListBrokerPositions 同源）
	// D-118：批量预取 per-venue triple_swap_day（从 OPEN legs 提取 venue 列表）
	tripleDayByVenue := s.prefetchTripleSwapDays(ctx, pbPositions, legsByPos)
	now := time.Now()
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] {
			continue // CLOSED/FAILED 不 enrich
		}
		legs := legsByPos[pb.Id]
		enrichOne(ctx, s, pb, legs, brokerSnap, now, tripleDayByVenue)
	}
}

// prefetchTripleSwapDays D-118：从活跃持仓的 OPEN legs 提取 venue 列表，批量查 facts 表 triple_swap_day。
// 返回 venueKey→time.Weekday map；未查到/查询失败 → 该 venue 不在 map 中（enrichOne 视为 tripleDay 不存在，退化为纯结算天数）。
func (s *Service) prefetchTripleSwapDays(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) map[string]time.Weekday {
	out := make(map[string]time.Weekday)
	if s.store == nil {
		return out // 测试未接线 → 空 map（enrichOne 退化为纯结算天数）
	}
	// 提取活跃持仓的 OPEN legs venue 集合
	venues := make(map[string]bool)
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] || !carryTypes[pb.Type] {
			continue
		}
		for _, leg := range legsByPos[pb.Id] {
			if leg.Phase == "OPEN" {
				venues[leg.Venue] = true
			}
		}
	}
	// 逐 venue 查 triple_swap_day（venue 级，symbol=venue 名）
	// facts 表 venue 存纯数字名（如 "REDACTED_MT_ACCT"），legs venue 存 "REDACTED_MT_ACCT:mt5" → 去后缀查
	for venueKey := range venues {
		venueName := venueFromKey(venueKey)
		facts, err := s.store.LatestFacts(ctx, fact.KindTripleSwapDay, venueName, venueName)
		if err == nil && len(facts) > 0 {
			out[venueKey] = time.Weekday(int(facts[0].Value))
		}
		// 查询失败/无数据 → 不入 map（enrichOne 视为无三倍日）
	}
	return out
}

// enrichOne 单行派生（拆出便于测试）。
// D-118：tripleDayByVenue 为 per-venue triple_swap_day 预取 map（可能空，enrichOne 退化为纯结算天数）。
func enrichOne(ctx context.Context, s *Service, pb *v1.Position, legs []pgstore.PositionLeg, brokerSnap []*v1.BrokerPosition, now time.Time, tripleDayByVenue map[string]time.Weekday) {
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

// pickTripleDay D-118：从 OPEN legs 的 venue 中取三倍日（任一腿有三倍日即用，多腿一致时取第一个）。
// 返回 (weekday, found)；tripleDayByVenue 无该 venue → found=false（退化为纯结算天数）。
func pickTripleDay(openLegs []pgstore.PositionLeg, tripleDayByVenue map[string]time.Weekday) (time.Weekday, bool) {
	for _, leg := range openLegs {
		if td, ok := tripleDayByVenue[leg.Venue]; ok {
			return td, true
		}
	}
	return 0, false
}

// openedAtTime D-118：从 pb.OpenedAt 或 OPEN FILLED legs 的最早 ts 取开仓时间。
// 与 holdDaysToNow 同构（提取公共部分，避免重复逻辑）。
func openedAtTime(pb *v1.Position, openLegs []pgstore.PositionLeg) time.Time {
	if pb.OpenedAt != nil {
		return pb.OpenedAt.AsTime()
	}
	var minTs time.Time
	for _, l := range openLegs {
		if l.Status == "FILLED" {
			if minTs.IsZero() || l.Ts.Before(minTs) {
				minTs = l.Ts
			}
		}
	}
	return minTs
}

// settlementDays D-118：计算 [openedAt, now) 区间的结算天数 + 三倍日数。
// 结算天数 = Mon-Fri 天数（排除周六周日，D-110 P1 确认周末不结算）。
// 三倍日数 = 该区间内 weekday==tripleDay 的天数（hasTriple=false 时为 0）。
// 算法：逐日遍历 openedAt 的日期部分到 now 的日期部分（不含 now 当天，结算发生在 UTC 21:00，当天未到结算时间不计）。
// 注意：这里按 UTC 日期遍历（MT5 swap 结算时间 21:00 UTC，日期边界按 UTC）。
func settlementDays(openedAt, now time.Time, tripleDay time.Weekday, hasTriple bool) (settleDays, tripleDays int) {
	// 按 UTC 日期对齐（结算发生在 21:00 UTC，日期按 UTC 算）
	start := openedAt.UTC()
	end := now.UTC()
	// 日期部分（截断到 00:00 UTC）
	startDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endDate := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	// 如果开仓当天已过 21:00 UTC，从次日开始算（当天已结算）
	if start.Hour() >= 21 {
		startDate = startDate.AddDate(0, 0, 1)
	}
	// 如果 now 当天未到 21:00 UTC，不算 now 当天（尚未结算）
	if end.Hour() < 21 {
		endDate = endDate.AddDate(0, 0, -1)
	}
	// 逐日遍历 [startDate, endDate]
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue // 周末不结算
		}
		settleDays++
		if hasTriple && wd == tripleDay {
			tripleDays++
		}
	}
	return settleDays, tripleDays
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
