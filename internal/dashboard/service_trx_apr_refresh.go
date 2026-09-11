// D-249 S3 / D-254：TRX 刷新 RPC handler。
// TrxVoteAprRefresh（D-249 S3，deprecated）仅踢 trx_apr；TrxAccountRefresh（D-254）双踢 trx_account+trx_apr。
package dashboard

import (
	"context"
	"errors"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TrxVoteAprRefresh D-249 S3（deprecated, use TrxAccountRefresh — D-254 双踢吸收 on-open 职责）：
// 前端 TRX tab 变可见时调本 RPC，后端 kick scheduler 立即补一轮 trx_apr，再回 LastTRXVoteApr。
// kickFn nil=未接线（测试场景）→ 跳过 kick 直接回最新行（不阻断前端展示）。
func (s *Service) TrxVoteAprRefresh(ctx context.Context, req *connect.Request[v1.TrxVoteAprRefreshRequest]) (*connect.Response[v1.TrxVoteAprRefreshResponse], error) {
	if s.kickFn != nil {
		s.kickFn("trx_apr")
	}
	apr, err := s.store.LastTRXVoteApr(ctx)
	if err != nil {
		// D-249 S6 R-G2：仅 ErrNoRows=空表→零值（前端「数据未就绪」）；
		// 其余 DB 错误透传 error（前端已有 catch 静默，不伤体验；原吞并一切错误=掩盖真故障）。
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewResponse(&v1.TrxVoteAprRefreshResponse{}), nil
		}
		return nil, err
	}
	resp := &v1.TrxVoteAprRefreshResponse{
		CurrentRate: apr.CurrentRate, MaxRate: apr.MaxRate, Spread: apr.Spread,
		FetchedAt: timestamppb.New(apr.FetchedAt),
	}
	if apr.CurrentSr != nil {
		resp.CurrentSr = *apr.CurrentSr
	}
	if apr.MaxSr != nil {
		resp.MaxSr = *apr.MaxSr
	}
	return connect.NewResponse(resp), nil
}

// TrxAccountRefresh D-254：手动刷新按钮+on-open 双踢 trx_account+trx_apr 后返回最新 fin_trx_account 全字段快照。
// 双踢补全「打开 TronLink 即拉新」效果（D-249 S3 on-open 仅踢 trx_apr，trx_account 30s 档未覆盖）。
// Q3 裁定：sun 单位 ÷1e6 转 double（对齐 HedgeAccounts 口径）；Q1 裁定：全 11 字段透出。
// kickFn nil=未接线（测试场景）→ 跳过 kick 直接回最新行（不阻断前端展示）。
// ErrNoRows=空表→零值响应（同 R-G2 口径，其余错误透传）。
func (s *Service) TrxAccountRefresh(ctx context.Context, req *connect.Request[v1.TrxAccountRefreshRequest]) (*connect.Response[v1.TrxAccountRefreshResponse], error) {
	if s.kickFn != nil {
		s.kickFn("trx_account")
		s.kickFn("trx_apr")
	}
	acc, err := s.store.LastTRXAccount(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connect.NewResponse(&v1.TrxAccountRefreshResponse{}), nil
		}
		return nil, err
	}
	resp := &v1.TrxAccountRefreshResponse{
		Id:              acc.ID,
		Address:         acc.Address,
		TrxBalance:      float64(acc.BalanceSun) / 1e6,
		TrxAllowance:    float64(acc.AllowanceSun) / 1e6,
		EnergyDelegated: float64(acc.EnergyDelegated) / 1e6,
		BandwidthStaked: float64(acc.BandwidthStaked) / 1e6,
		NetLimit:        float64(acc.NetLimit) / 1e6,
		Votes:           float64(acc.VotesTotal),
		FetchedAt:       timestamppb.New(acc.FetchedAt),
		// D-278 S5：地址资源显示
		NetUsed:         float64(acc.NetUsed) / 1e6,
		FreeNetUsed:     float64(acc.FreeNetUsed) / 1e6,
		FreeNetLimit:    float64(acc.FreeNetLimit) / 1e6,
		EnergyLimit:     float64(acc.EnergyLimit) / 1e6,
		EnergyUsed:      float64(acc.EnergyUsed) / 1e6,
		BandwidthStatus: bandwidthStatus(acc),
	}
	if acc.SRAddress != nil {
		resp.SrAddress = *acc.SRAddress
	}
	if len(acc.Unfrozen) > 0 {
		resp.Unfrozen = string(acc.Unfrozen)
	}
	return connect.NewResponse(resp), nil
}
