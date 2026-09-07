// D-249 S3：TrxVoteAprRefresh RPC handler——on-open 触发采集补轮 + 返回最新聚合行。
package dashboard

import (
	"context"
	"errors"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TrxVoteAprRefresh D-249 S3：前端 TRX tab 变可见时调本 RPC，
// 后端 kick scheduler 立即补一轮（D-060 F2 boot 竞态修复同款 Kick 机制），再回 LastTRXVoteApr。
// 解决 5min 轮询节流 vs TronLink 页面打开即刷新的体感差。
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
