// D-254：TRX 账户刷新 RPC handler（TrxAccountRefresh 双踢 trx_account+trx_apr）。
// D-249 TrxVoteAprRefresh 已于 D-294 清理退役（前端零调用，deprecated since D-254）。
package dashboard

import (
	"context"
	"errors"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TrxAccountRefresh D-254：手动刷新按钮+on-open 双踢 trx_account+trx_apr 后返回最新 fin_trx_account 全字段快照。
// 双踢补全「打开 TronLink 即拉新」效果（D-249 S3 on-open 仅踢 trx_apr，trx_account 30s 档未覆盖）。
// Q3 裁定：sun 单位 ÷1e6 转 double（对齐 fin_trx_account 行口径）；Q1 裁定：全 11 字段透出。
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
		TrxAllowance:    float64(acc.RewardNumSun) / 1e6, // D-285：reward_num_sun 链上真值（与 fin_trx_account 同源）
		EnergyDelegated: float64(acc.EnergyDelegated) / 1e6,
		BandwidthStaked: float64(acc.BandwidthStaked) / 1e6,
		NetLimit:        float64(acc.NetLimit),
		Votes:           float64(acc.VotesTotal),
		FetchedAt:       timestamppb.New(acc.FetchedAt),
		// D-278 S5：地址资源显示
		NetUsed:         float64(acc.NetUsed),
		FreeNetUsed:     float64(acc.FreeNetUsed),
		FreeNetLimit:    float64(acc.FreeNetLimit),
		EnergyLimit:     float64(acc.EnergyLimit),
		EnergyUsed:      float64(acc.EnergyUsed),
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
