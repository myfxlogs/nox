package autotrade

// D-140 S3：swap 组合公开 API（供 dashboard RPC 展示，basis_bps 只展示不动排序）。
// 独立文件避免 engine_swaparb.go 超 300 行。

import (
	"arb/internal/keydomain"
	"context"
)

// SwapComboPublic D-140 S3：公开 swap 组合条目（供 dashboard RPC 展示，无内部字段）。
// AliasLong/Short D-144 ③：别名（空=回退 venueKey，前端显示用）。
type SwapComboPublic struct {
	Canonical  string
	VenueLong  string
	VenueShort string
	NetSwapBps float64
	BasisBps   float64
	AliasLong  string // D-144 ③
	AliasShort string // D-144 ③
}

// DiscoverSwapCombosPublic D-140 S3：公开 discoverSwapCombos 供 dashboard RPC 展示。
// 返回 SwapComboPublic 列表（排序仍按 netSwapBps，basis 只展示不动排序）。
// D-144 ③：从 store 加载 venue→alias 映射，填充 AliasLong/Short（venueKey="{venue}:{src}" 取冒号前 venue 查别名）。
func (e *Engine) DiscoverSwapCombosPublic(ctx context.Context) []SwapComboPublic {
	combos := e.discoverSwapCombos(ctx)
	// D-144 ③：加载别名映射（best-effort，查询失败 → 空 map，alias 留空前端回退 venueKey）
	aliasMap, _ := e.store.ListAccountAliases(ctx)
	out := make([]SwapComboPublic, 0, len(combos))
	for _, c := range combos {
		out = append(out, SwapComboPublic{
			Canonical:  c.canonical,
			VenueLong:  c.vLong,
			VenueShort: c.vShort,
			NetSwapBps: c.netSwapBps,
			BasisBps:   c.basisBps,
			AliasLong:  aliasForVenueKey(aliasMap, c.vLong),  // D-144 ③
			AliasShort: aliasForVenueKey(aliasMap, c.vShort), // D-144 ③
		})
	}
	return out
}

// aliasForVenueKey D-144 ③：从 venueKey="{venue}:{src}" 提取 venue 查 alias。
// venueKey 无冒号 → 整串当 venue 查；alias 空 → 返回空（前端回退 venueKey）。
func aliasForVenueKey(aliasMap map[string]string, venueKey string) string {
	venue := keydomain.BareOf(venueKey)
	return aliasMap[venue]
}
