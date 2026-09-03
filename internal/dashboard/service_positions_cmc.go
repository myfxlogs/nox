package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/position"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// applyCMCPositionFields 从 fin_cmc_positions 填真值 + ComputeCMCPositionPrice（D-208 v2）。
// EntryPrice/Leverage/OpenedAt/Qty 全部从 CMC 持仓详情填（视图全自动）。
func applyCMCPositionFields(pb *v1.PersonalPosition, p *pgstore.PersonalPosition, cmcSrc position.CMCSource, freshTTL time.Duration, now time.Time) {
	if cmcPos, err := cmcSrc.LastCMCPosition(p.CMCInstrumentID); err == nil {
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

// ListCMCAccount 返回 CMC 账户摘要 + 最近 50 条流水（D-208 v2-C：前端个人持仓卡头部摘要行 + 流水小表）。
// 摘要空表 → summary=nil（前端显示「CMC 数据未就绪」）；流水空表 → history 空数组。
func (s *Service) ListCMCAccount(ctx context.Context, req *connect.Request[v1.ListCMCAccountRequest]) (*connect.Response[v1.ListCMCAccountResponse], error) {
	resp := &v1.ListCMCAccountResponse{}

	// 摘要（空表容忍——CMC 采集器未跑或表空时 summary=nil）
	summary, err := s.store.LastCMCAccountSummary(ctx)
	if err == nil {
		resp.Summary = &v1.CMCAccountInfo{
			TotalAccountValue:     summary.TotalAccountValue,
			TotalCashBalance:      summary.TotalCashBalance,
			TotalUnrealisedPnl:    summary.TotalUnrealisedPnL,
			CloseoutPrice:         summary.CloseoutPrice,
			DistanceToCloseoutPct: summary.DistanceToCloseoutPct,
			FetchedAt:             timestamppb.New(summary.FetchedAt),
		}
	}
	// ErrNoRows = 空表，summary 保持 nil（非错误）

	// 流水（最近 50 条倒序）
	history, err := s.store.ListCMCHistory(ctx, summary.AccountID, 50)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list cmc history: %w", err))
	}
	for _, h := range history {
		item := &v1.CMCHistoryItem{
			TransactionId:   h.TransactionID,
			TransactionType: h.TransactionType,
			Amount:          h.Amount,
			Balance:         h.Balance,
			Currency:        h.Currency,
			OrderId:         h.OrderID,
			TransactionTime: timestamppb.New(h.TransactionTime),
		}
		resp.History = append(resp.History, item)
	}

	return connect.NewResponse(resp), nil
}
