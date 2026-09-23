// service_margin_rpc.go —— D-405 S6：ListAccountMarginStatus RPC（财务页「账户资金与保证金」卡）。
// 数据源=fin_venue_margin_status（marginGuard 每轮 upsert）× fin_venue_account_snapshot 最新行 join；
// 天数/建议额/档位全后端给，前端零计算。
package dashboard

import (
	"context"
	"log"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

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
	snaps, err := s.store.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		log.Printf("[margin-status] latest snapshots: %v", err) // 快照缺仅少展示列，不毁 RPC
	}
	snapMap := make(map[string]pgstore.VenueAccountLatest, len(snaps))
	for _, sn := range snaps {
		snapMap[sn.Venue+"|"+sn.VenueType] = sn
	}
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
		if sn, ok := snapMap[r.Venue+"|"+r.VenueType]; ok {
			row.Currency = sn.Currency
			if sn.BalanceRaw != nil {
				row.Balance = *sn.BalanceRaw
			}
			if sn.EquityRaw != nil {
				row.Equity = *sn.EquityRaw
			}
			switch marginVenueClass(r.VenueType) {
			case "cfd":
				if sn.FreeMarginRaw != nil {
					row.FreeMargin = *sn.FreeMarginRaw
					if sn.EquityRaw != nil {
						row.MarginUsed = *sn.EquityRaw - *sn.FreeMarginRaw
					}
				}
			case "cex":
				// 可用头room≈equity−MMR；已用≈MMR（IMR 未采，如实近似）
				if sn.EquityRaw != nil && sn.BufferMultiple != nil && *sn.BufferMultiple > 0 {
					mmr := *sn.EquityRaw / *sn.BufferMultiple
					row.MarginUsed = mmr
					row.FreeMargin = *sn.EquityRaw - mmr
					if row.FreeMargin < 0 {
						row.FreeMargin = 0
					}
				}
			}
		}
		out = append(out, row)
	}
	return connect.NewResponse(&v1.ListAccountMarginStatusResponse{Rows: out}), nil
}
