package dashboard

// service_pnl_anchor_float.go D-141：「当前浮动」=Σ 平仓净估（全平到手口径）。
// Σ active 仓 close_now_net_usd（D-480 enrich 产物复用，零重算分叉）
// + Σ 游离 broker 腿 close_now_estimate_usd（D-009 S3；PositionId 未标注=外单/事故仓兜底）。
// fail-closed：任一仓/腿净估缺 → complete=false（调用方落 available=false 全格「—」）。

import (
	"context"

	v1 "arb/internal/gen/dashboardv1"
)

// floatingCloseNowNet 全平净估合计。返回 (合计, 计入行数, 全部可得)。
// n=0 且 complete=true = 真零仓（0 合法显示，非「—」）。
func (s *Service) floatingCloseNowNet(ctx context.Context) (sum float64, n int, complete bool) {
	complete = true
	// ① active arb 仓：assemblePositions 同源 enrich（status=ACTIVE 覆盖 OPENING/OPEN/LEGGED/CLOSING 四态）。
	pbs, _, err := s.assemblePositions(ctx, "ACTIVE", 500, 0)
	if err != nil {
		return 0, 0, false
	}
	for _, pb := range pbs {
		if pb.CloseNowNetUsd == nil {
			complete = false // 仓级净估缺（缺报价/未匹配零实收）→ 不全平可信
			continue
		}
		sum += *pb.CloseNowNetUsd
		n++
	}
	// ② 游离 broker 腿：position_id 标注后仍 PositionId==0 = 外单/事故仓（含 OPENING/CLOSING
	// 覆盖——D-141 把 buildPositionIDIndex 扩 ACTIVE 四态，过渡态不再误判游离双计）。
	bpos := s.bpm.Snapshot()
	annotatePositionID(ctx, s.store, bpos)
	s.annotateCloseEstimate(ctx, bpos)
	for _, bp := range bpos {
		if bp.PositionId != 0 {
			continue
		}
		if bp.CloseNowEstimateUsd == nil {
			complete = false // 游离腿估不出（报价非 fresh/输入缺）→ 不显示冻结值
			continue
		}
		sum += *bp.CloseNowEstimateUsd
		n++
	}
	return sum, n, complete
}

// 显式引用担保 proto 字段契约（防漂移）。
var _ = (*v1.Position).GetCloseNowNetUsd
var _ = (*v1.BrokerPosition).GetCloseNowEstimateUsd
