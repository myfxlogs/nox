package dashboard

import (
	"context"
	"strings"

	"arb/internal/platform/combo"
	"arb/internal/platform/usdnotional"
	"arb/internal/store/pgstore"
)

// canonicalFromComboKey 从组合键/opportunity_id 派生 canonical（D-132 T1）。
// combo key 格式 = strategy:canonical:venueBuy:venueSell（venue 含 :type 后缀整体为一段，6 段）。
// 旧 tick 格式（尾段纯数字时间戳，7 段）先经 combo.FromOppID 剥离为 6 段组合键。
// canonical = 第 2 段（index 1）；异常输入 → 空串（调用方跳过该腿）。
func canonicalFromComboKey(oppID string) string {
	ck := combo.FromOppID(oppID)
	parts := strings.Split(ck, ":")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// swapArbVenueExposureCheck 纯函数：判断新仓加入后两腿 venue SWAPARB 合计裸露是否在 cap 内（D-132 T1）。
// D-174：按保证金口径（名义/杠杆 vs 权益×cap）。cap 语义=保证金/权益比。
// existingLegs 为当前活跃 SWAPARB 仓的 OPEN 腿；canonicalOf 返回腿所属持仓的 canonical（空=跳过）；
// usdPerLotOf 返回 (canonical, venue) 每 EffectiveQty 单位 USD 名义——MT 腿=1 手（同 USDPerLotForLeg
// "ask" 口径），CEX 腿=1 基础币（EffectiveQty 已是基础币，勿乘 ct_val，D-311 R5/R6）；ok=false 跳过该腿。
// longLev/shortLev = 各腿 venue 杠杆（调用方经 effectiveLeverage 处理后传入，≤0 回落 1.0）。
// cap ≤ 0 → 不启用（true）；equity ≤ 0 → fail-closed（false）。
// 新仓名义加到两腿 venue；任一 venue 保证金 = 合计名义/杠杆 > equity×cap → false（skip）。
func swapArbVenueExposureCheck(
	existingLegs []pgstore.PositionLeg,
	canonicalOf func(posID int64) string,
	usdPerLotOf func(canonical, venue string) (float64, bool),
	venueLong, venueShort string,
	newNotional, eqLong, eqShort, cap, longLev, shortLev float64,
) bool {
	if cap <= 0 {
		return true
	}
	if eqLong <= 0 || eqShort <= 0 {
		return false
	}
	exposure := make(map[string]float64)
	for _, leg := range existingLegs {
		if leg.Phase != "OPEN" || leg.Qty <= 0 {
			continue
		}
		canonical := canonicalOf(leg.PositionID)
		if canonical == "" {
			continue
		}
		usdPerLot, ok := usdPerLotOf(canonical, leg.Venue)
		if !ok || usdPerLot <= 0 {
			continue
		}
		// D-311 R6：EffectiveQty=成交口径（CEX=基础币/MT=手数），与 usdPerLotOf 分派同单位。
		exposure[leg.Venue] += leg.EffectiveQty() * usdPerLot
	}
	longExposure := exposure[venueLong] + newNotional
	shortExposure := exposure[venueShort] + newNotional
	// D-174：按保证金口径（名义/杠杆 vs 权益×cap）。cap 语义=保证金/权益比。
	longMargin := longExposure / longLev
	shortMargin := shortExposure / shortLev
	return longMargin <= eqLong*cap && shortMargin <= eqShort*cap
}

// SwapArbVenueExposureOK 判断新开 SWAPARB 仓后两腿 venue 合计裸露是否在 cap 内（D-132 T1 venue 级敞口门）。
// cap ≤ 0 → 不启用（true）；venue 离线/币种非 USD → fail-closed（false，与 SwapArbNotional 同判）。
// 聚合活仓 OPEN 腿名义 = qty × 单位名义（MT=USDPerLotForLeg "ask" 口径；CEX=price×fx，D-311 R5）。
// store 未接线（测试）→ 仅 cap+equity 门生效，放行（无活仓可聚合）。
func (s *Service) SwapArbVenueExposureOK(ctx context.Context, venueLong, venueShort string, newNotional float64) bool {
	if s.swapArbVenueExposureCap <= 0 {
		return true
	}
	eqLong, curLong, onlineLong := s.AccountEquityUSD(venueLong)
	if !onlineLong || curLong != "USD" {
		return false
	}
	eqShort, curShort, onlineShort := s.AccountEquityUSD(venueShort)
	if !onlineShort || curShort != "USD" {
		return false
	}
	if s.store == nil {
		return true
	}
	positions, err := s.store.ListActivePositions(ctx)
	if err != nil {
		return false
	}
	canonicalByPos := make(map[int64]string)
	var swapArbPosIDs []int64
	for _, p := range positions {
		if p.Type != "SWAPARB" {
			continue
		}
		canonicalByPos[p.ID] = canonicalFromComboKey(p.OpportunityID)
		swapArbPosIDs = append(swapArbPosIDs, p.ID)
	}
	var legs []pgstore.PositionLeg
	if len(swapArbPosIDs) > 0 {
		legs, err = s.store.ListOpenLegsByPositionIDs(ctx, swapArbPosIDs)
		if err != nil {
			return false
		}
	}
	canonicalOf := func(posID int64) string { return canonicalByPos[posID] }
	usdPerLotOf := func(canonical, venue string) (float64, bool) {
		// D-311 R5 缺陷#6b：CEX 腿持久化 qty=基础币 → 单位名义=price×fx（cs=1），
		// 勿乘 ct_val（ct_val=1000 腿敞口曾放千倍→新 SWAPARB 被静默过度拦截）；
		// MT 腿 qty=手数 → 维持 per-lot（price×cs×fx）。
		if !isMTVenue(venue) {
			q, ok := s.qbus.Latest(canonical, venue)
			if !ok {
				return 0, false
			}
			return usdnotional.PerLot(canonical, q.Ask, 1.0, s.qbus)
		}
		return s.USDPerLotForLeg(canonical, venue, "ask")
	}
	// D-174：各腿用自己的杠杆（venue 可能杠杆不同）。≤0/未知 → 回落 1.0（保守）；
	// ∞ 哨兵（≥1e6）保留（除法得 ~0 保证金，保守通过）。
	longLev, _ := s.AccountLeverage(venueLong)
	shortLev, _ := s.AccountLeverage(venueShort)
	return swapArbVenueExposureCheck(legs, canonicalOf, usdPerLotOf, venueLong, venueShort, newNotional, eqLong, eqShort, s.swapArbVenueExposureCap, safeLegLeverage(longLev), safeLegLeverage(shortLev))
}

// safeLegLeverage D-174：venue exposure 各腿杠杆安全化。
// ≤0（未知/离线）→ 1.0（保守回落，无杠杆）；≥1e6（∞ 哨兵）保留（除法得 ~0 保证金）；
// 其余原值返回。
func safeLegLeverage(lev float64) float64 {
	if lev <= 0 {
		return 1.0
	}
	return lev
}
