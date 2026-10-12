// service_margin_rpc.go —— D-405 S6：ListAccountMarginStatus RPC（财务页「账户资金与保证金」卡）。
// 数据源=fin_venue_margin_status（marginGuard 每轮 upsert）× fin_venue_account_snapshot 最新行 join；
// 天数/建议额/档位全后端给，前端零计算。
package dashboard

import (
	"context"
	"log"

	"arb/internal/data/keydomain"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/portfolio/capitalview"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListAccountMarginStatus 逐账户保证金档位行（margin_status 为主表；snapshot 提供
// balance/equity/free_margin 展示列——status 行缺=未评估 venue 不出卡，诚实呈现）。
func (s *Service) ListAccountMarginStatus(ctx context.Context, _ *connect.Request[v1.ListAccountMarginStatusRequest]) (*connect.Response[v1.ListAccountMarginStatusResponse], error) {
	rows, err := s.store.ListVenueMarginStatus(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// D-549：展示列走统一读口 capitalview.Live（CEX=活账本/REST 实拉；快照兜底带龄注记）；
	// 批量预取快照面省每行一查；资金列缺=如实空（不毁 RPC）。
	snaps, err := s.liveAccountLatest(ctx)
	if err != nil {
		log.Printf("[margin-status] latest snapshots: %v", err)
	}
	cv := s.capitalView()
	out := make([]*v1.AccountMarginStatus, 0, len(rows))
	for _, r := range rows {
		row := &v1.AccountMarginStatus{
			Venue: r.Venue, VenueType: r.VenueType, Tier: r.Tier,
			UpdatedAt: timestamppb.New(r.UpdatedAt),
		}
		row.AssetType = "cex"
		if marginVenueClass(r.VenueType) == "cfd" {
			row.AssetType = "cfd"
		}
		if r.CoverageDays != nil {
			row.CoverageDays = *r.CoverageDays
		}
		if r.SuggestedTopupUSD != nil {
			row.SuggestedTopupUsd = *r.SuggestedTopupUSD
		}
		f, ok := cv.LiveSnaps(ctx, keydomain.Join(r.Venue, r.VenueType), snaps)
		if !ok {
			out = append(out, row)
			continue
		}
		row.Currency = f.Currency
		row.Equity = f.Equity
		row.Balance = f.Equity // proto 口径 CEX 余额=总权益；快照行有 balance_raw 时覆盖为实采
		if f.HasBalance {
			row.Balance = f.Balance
		}
		switch marginVenueClass(r.VenueType) {
		case "cfd":
			if f.HasAvailable {
				row.FreeMargin = f.Available
				row.MarginUsed = f.Equity - f.Available
			}
		case "cex":
			if f.HasAvailable {
				row.FreeMargin = f.Available
				row.MarginUsed = f.Equity - f.Available
				if row.MarginUsed < 0 {
					row.MarginUsed = 0
				}
				break
			}
			// 可用未采：头room≈equity−MMR（IMR 未采，如实近似；buffer 仅快照面有）。
			if sn, sok := capitalview.SnapPick(snaps, keydomain.Join(r.Venue, r.VenueType)); sok &&
				sn.EquityRaw != nil && sn.BufferMultiple != nil && *sn.BufferMultiple > 0 {
				mmr := *sn.EquityRaw / *sn.BufferMultiple
				row.MarginUsed = mmr
				row.FreeMargin = *sn.EquityRaw - mmr
				if row.FreeMargin < 0 {
					row.FreeMargin = 0
				}
			}
		}
		out = append(out, row)
	}
	return connect.NewResponse(&v1.ListAccountMarginStatusResponse{Rows: out}), nil
}
