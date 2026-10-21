// xasset_carry_helpers.go —— 自 xasset_carry.go 拆出（300 行棘轮；纯搬移零语义改）。
package detector

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
