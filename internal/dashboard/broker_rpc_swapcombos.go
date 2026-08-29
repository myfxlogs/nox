package dashboard

// D-140 S3：swap 组合列表 RPC（basis_bps 只展示不动排序）。

import (
	"context"
	"errors"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// SwapComboEntry swap 组合条目（D-140 S3，dashboard↔autotrade 适配层无依赖环）。
type SwapComboEntry struct {
	Canonical  string
	VenueLong  string
	VenueShort string
	NetSwapBps float64
	BasisBps   float64
}

// SetSwapComboLister D-140 S3：main 装配 swap 组合列表注入。
func (s *Service) SetSwapComboLister(fn func() []SwapComboEntry) { s.swapComboLister = fn }

// ListSwapCombos D-140 S3：返回 swap 组合列表（basis_bps 只展示不动排序/门槛/触发）。
// nil lister → 空列表（未接线不报错）。
func (s *Service) ListSwapCombos(ctx context.Context, req *connect.Request[v1.ListSwapCombosRequest]) (*connect.Response[v1.ListSwapCombosResponse], error) {
	if s.swapComboLister == nil {
		return connect.NewResponse(&v1.ListSwapCombosResponse{}), nil
	}
	entries := s.swapComboLister()
	combos := make([]*v1.SwapCombo, 0, len(entries))
	for _, e := range entries {
		combos = append(combos, &v1.SwapCombo{
			Canonical:  e.Canonical,
			VenueLong:  e.VenueLong,
			VenueShort: e.VenueShort,
			NetSwapBps: e.NetSwapBps,
			BasisBps:   e.BasisBps,
		})
	}
	return connect.NewResponse(&v1.ListSwapCombosResponse{Combos: combos}), nil
}

// _ 编译期断言：errors 包被使用（未来扩展错误处理）
var _ = errors.New
