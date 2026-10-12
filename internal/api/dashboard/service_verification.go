package dashboard

import (
	"context"
	"log"
	"math"

	"arb/internal/review/verification"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fillVerifiedFields D-086 按 vkey 查 verification_stats 填入机会 proto 的验证字段。
// store nil 或查询失败 → V0 零值（fail-safe，前端灰显「预估」）。
// 阈值同 engine 发布门（W/N/R 走 config，但 proto 层只填原始值+level，前端按 level 渲染）。
func (s *Service) fillVerifiedFields(pb *v1.Opportunity, o *pgstore.Opportunity) {
	if s.store == nil {
		return // 测试无 store → V0 零值
	}
	vkey := verification.Vkey(o.Type, o.VenueBuy, o.VenueSell)
	samples, ratio, fresh, err := s.store.VerificationFor(context.Background(), vkey, s.frontCfg.VerifyWindowDays)
	if err != nil {
		log.Printf("[dashboard] verification query %s: %v (V0 兜底)", vkey, err)
		return
	}
	pb.VerifySamples = int32(samples)
	if !math.IsNaN(ratio) {
		pb.VerifyRatio = ratio
	}
	pb.VerifiedLevel = int32(verification.VerifyLevel(samples, s.frontCfg.VerifyMinSamples, ratio, s.frontCfg.VerifyMinRatio, fresh))
}

// ListVerificationStats D-086 验证统计全行（前端折叠区/归因诊断展示面）。
func (s *Service) ListVerificationStats(ctx context.Context, req *connect.Request[v1.ListVerificationStatsRequest]) (*connect.Response[v1.ListVerificationStatsResponse], error) {
	stats, err := s.store.ListVerificationStats(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbStats []*v1.VerificationStat
	for _, st := range stats {
		pbSt := &v1.VerificationStat{
			Vkey:        st.Vkey,
			Samples:     int32(st.Samples),
			ExpectedUsd: st.ExpectedUsd,
			RealizedUsd: st.RealizedUsd,
		}
		if st.ExpectedUsd > 0 {
			pbSt.Ratio = st.RealizedUsd / st.ExpectedUsd
		}
		if st.LastSampleAt != nil {
			pbSt.LastSampleAt = timestamppb.New(*st.LastSampleAt)
		}
		pbStats = append(pbStats, pbSt)
	}
	return connect.NewResponse(&v1.ListVerificationStatsResponse{Stats: pbStats}), nil
}
