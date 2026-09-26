package dashboard

import (
	"time"

	"arb/internal/position"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// applyCMCPositionFields 从 fin_cmc_positions 填真值 + ComputeCMCPositionPrice（D-208 v2）。
// EntryPrice/Leverage/OpenedAt/Qty 全部从 CMC 持仓详情填（视图全自动）。
func applyCMCPositionFields(pb *v1.PersonalPosition, p *pgstore.PersonalPosition, cmcSrc position.CMCSource, freshTTL time.Duration, now time.Time) {
	if cmcPos, err := cmcSrc.LastCMCPosition(*p.CMCInstrumentID); err == nil {
		if cmcPos.EntryPrice > 0 {
			pb.EntryPrice = cmcPos.EntryPrice
		}
		if cmcPos.Quantity > 0 {
			pb.Qty = cmcPos.Quantity
		}
		if cmcPos.OpenedAt != nil {
			pb.OpenedAt = timestamppb.New(*cmcPos.OpenedAt)
		}
		// leverage 从 margin_percentage 推导：L = 1/margin_percentage
		if cmcPos.MarginPercentage > 0 {
			pb.Leverage = int32(1.0 / cmcPos.MarginPercentage)
		}
	}
	v := position.ComputeCMCPositionPrice(p, cmcSrc, freshTTL, now)
	pb.Price = v.Price
	pb.Level = v.Level
	pb.PnlUsd = v.PnlUsd
	pb.LiqPrice = v.LiqPrice
	pb.DistToLiqPct = v.DistToLiqPct
}

// ListCMCAccount RPC 已在 D-212 退役（统一流水卡替代）。CMC 账户摘要+流水由 GetHedgeStructure 全量吸收。
