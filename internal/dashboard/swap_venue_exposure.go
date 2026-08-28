package dashboard

import (
	"context"
	"strings"

	"arb/internal/combo"
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
// existingLegs 为当前活跃 SWAPARB 仓的 OPEN 腿；canonicalOf 返回腿所属持仓的 canonical（空=跳过）；
// usdPerLotOf 返回 (canonical, venue) 1 手 USD 名义（同 USDPerLotForLeg "ask" 口径，ok=false 跳过该腿）。
// cap ≤ 0 → 不启用（true）；equity ≤ 0 → fail-closed（false）。
// 新仓名义加到两腿 venue；任一 venue 合计 > equity×cap → false（skip）。
func swapArbVenueExposureCheck(
	existingLegs []pgstore.PositionLeg,
	canonicalOf func(posID int64) string,
	usdPerLotOf func(canonical, venue string) (float64, bool),
	venueLong, venueShort string,
	newNotional, eqLong, eqShort, cap float64,
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
		exposure[leg.Venue] += leg.Qty * usdPerLot
	}
	longExposure := exposure[venueLong] + newNotional
	shortExposure := exposure[venueShort] + newNotional
	return longExposure <= eqLong*cap && shortExposure <= eqShort*cap
}

// SwapArbVenueExposureOK 判断新开 SWAPARB 仓后两腿 venue 合计裸露是否在 cap 内（D-132 T1 venue 级敞口门）。
// cap ≤ 0 → 不启用（true）；venue 离线/币种非 USD → fail-closed（false，与 SwapArbNotional 同判）。
// 聚合活仓 OPEN 腿名义 = qty × USDPerLotForLeg(canonical, venue, "ask")（同 trySwapArb sizing 口径）。
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
		return s.USDPerLotForLeg(canonical, venue, "ask")
	}
	return swapArbVenueExposureCheck(legs, canonicalOf, usdPerLotOf, venueLong, venueShort, newNotional, eqLong, eqShort, s.swapArbVenueExposureCap)
}
