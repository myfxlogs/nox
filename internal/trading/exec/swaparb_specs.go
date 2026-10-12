// swaparb_specs.go —— D-515 §3：swaparb_allow_combos 行 → ComboAllowSpec 映射单源。
// NULL 限额 → 0=不限（与 ParseComboAllow 空字段语义一致）；engine/pipeline/dashboard
// 三读侧同此映射禁复制（D-367 XAssetSpecsFromRows 先例）。
package exec

import "nox/internal/store/pgstore"

// SwapArbSpecsFromRows 表行 → spec 列表（NULL → 0；enabled 过滤在 SQL 层）。
func SwapArbSpecsFromRows(rows []pgstore.SwapArbAllowRow) []ComboAllowSpec {
	out := make([]ComboAllowSpec, 0, len(rows))
	for _, r := range rows {
		spec := ComboAllowSpec{
			Canonical:  r.Canonical,
			VenueLong:  r.VenueLong,
			VenueShort: r.VenueShort,
		}
		if r.MaxLongQty != nil {
			spec.MaxLongQty = *r.MaxLongQty
		}
		if r.MaxShortQty != nil {
			spec.MaxShortQty = *r.MaxShortQty
		}
		if r.MaxGrossUSD != nil {
			spec.MaxGrossExposureUSD = *r.MaxGrossUSD
		}
		out = append(out, spec)
	}
	return out
}
