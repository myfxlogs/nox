// xasset_carry_helpers.go —— 自 xasset_carry.go 拆出（300 行棘轮；纯搬移零语义改）。
package detector

import "time"

// swapAnnualPct MT 腿 swap 年化百分点：long=true 取 swap_long[0]，false 取 swap_short[1]；
// bps_daily × 365 / 100 → 年化 %（负值=付息自然为净息差负项）。非 MT 腿/无 fact → 0。
func swapAnnualPct(swap map[string]map[string][2]float64, canonical string, e AssetEquivEntry, long bool) float64 {
	if e.VenueType != "mt4" && e.VenueType != "mt5" {
		return 0
	}
	pair, ok := lookupSwap(swap, canonical, e.Venue)
	if !ok {
		return 0
	}
	if long {
		return pair[0] * 365 / 100
	}
	return pair[1] * 365 / 100
}

// delivFreshTTL 交割腿鲜度宽档：远月 okx tickers 频道帧距实证 16-35min
// （STATE 行44——on-change 推送天然节拍非断流），宽档 45min=峰值帧距+容差；
// 真断流仍 <1h 检出（真断流检测语义不降）。
const delivFreshTTL = 45 * time.Minute

// legFreshTTL 腿型分档：futures→delivFreshTTL；其余（spot/perp/mt 族）=主阈零动。
func legFreshTTL(base time.Duration, venueType string) time.Duration {
	if venueType == "futures" {
		return delivFreshTTL
	}
	return base
}
