package autotrade

// D-140 S3：swap 组合公开 API（供 dashboard RPC 展示，basis_bps 只展示不动排序）。
// 独立文件避免 engine_swaparb.go 超 300 行。

import "context"

// SwapComboPublic D-140 S3：公开 swap 组合条目（供 dashboard RPC 展示，无内部字段）。
type SwapComboPublic struct {
	Canonical  string
	VenueLong  string
	VenueShort string
	NetSwapBps float64
	BasisBps   float64
}

// DiscoverSwapCombosPublic D-140 S3：公开 discoverSwapCombos 供 dashboard RPC 展示。
// 返回 SwapComboPublic 列表（排序仍按 netSwapBps，basis 只展示不动排序）。
func (e *Engine) DiscoverSwapCombosPublic(ctx context.Context) []SwapComboPublic {
	combos := e.discoverSwapCombos(ctx)
	out := make([]SwapComboPublic, 0, len(combos))
	for _, c := range combos {
		out = append(out, SwapComboPublic{
			Canonical:  c.canonical,
			VenueLong:  c.vLong,
			VenueShort: c.vShort,
			NetSwapBps: c.netSwapBps,
			BasisBps:   c.basisBps,
		})
	}
	return out
}
