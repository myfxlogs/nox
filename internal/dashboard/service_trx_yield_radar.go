// D-258 S3：收益率雷达 RPC handler（GetTrxYieldRadar）。
package dashboard

import (
	"context"
	"errors"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SetTrxFloorPct D-258 S3：注入 TRX 能量租金率 floor 告警阈值（main 装配后调）。
func (s *Service) SetTrxFloorPct(pct float64) { s.trxFloorPct = pct }

// GetTrxYieldRadar D-258 S3：收益率雷达——后端算好，前端零计算（D-043 前端零信任）。
// 字段：能量 7d/30d APR + 投票当前/30d 均值 + floor + 样本天数 + updated_at。
// 冷启动（无数据）→ 全零响应（前端「数据未就绪」），不猜不报错。
func (s *Service) GetTrxYieldRadar(ctx context.Context, req *connect.Request[v1.GetTrxYieldRadarRequest]) (*connect.Response[v1.GetTrxYieldRadarResponse], error) {
	// 能量 7d APR
	apr7d, sample7d, err := s.TrxEnergyRentalAPR(ctx, 7)
	if err != nil {
		return nil, err
	}
	// 能量 30d APR
	apr30d, sample30d, err := s.TrxEnergyRentalAPR(ctx, 30)
	if err != nil {
		return nil, err
	}
	// 投票当前 APR（最新 fin_trx_vote_apr.current_rate）
	var voteCurrent float64
	vote, err := s.store.LastTRXVoteApr(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		voteCurrent = vote.CurrentRate
	}
	// 投票 30d 均值
	var vote30dAvg float64
	now := timestamppb.Now().AsTime()
	from30d := now.AddDate(0, 0, -30)
	avg, ok, err := s.store.AvgTRXVoteAprByRange(ctx, from30d, now)
	if err != nil {
		return nil, err
	}
	if ok {
		vote30dAvg = avg
	}
	// updated_at：取最新投票 APR fetched_at 或当前时间
	var updatedAt = now
	if err == nil && !vote.FetchedAt.IsZero() {
		updatedAt = vote.FetchedAt
	}
	return connect.NewResponse(&v1.GetTrxYieldRadarResponse{
		EnergyApr_7D:   apr7d,
		EnergyApr_30D:  apr30d,
		VoteAprCurrent: voteCurrent,
		VoteApr_30DAvg: vote30dAvg,
		FloorPct:       s.trxFloorPct,
		SampleDays_7D:  int32(sample7d),
		SampleDays_30D: int32(sample30d),
		UpdatedAt:      timestamppb.New(updatedAt),
	}), nil
}
