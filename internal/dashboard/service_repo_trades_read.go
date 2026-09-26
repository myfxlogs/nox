// D-272 逆回购只读 RPC handlers（从 service_repo_trades.go 拆出，保 ≤300 行）。
// ListRepoTrades + GetRepoStrategyValidation。
package dashboard

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
)

// ListRepoTrades 列出逆回购操作历史。
func (s *Service) ListRepoTrades(ctx context.Context, req *connect.Request[v1.ListRepoTradesRequest]) (*connect.Response[v1.ListRepoTradesResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	status := ""
	if req.Msg.Status != nil {
		status = *req.Msg.Status
	}
	rows, err := s.store.ListRepoTrades(ctx, limit, status)
	if err != nil {
		return nil, err
	}
	resp := &v1.ListRepoTradesResponse{}
	for _, r := range rows {
		row := &v1.RepoTradeRow{
			Id:             r.ID,
			InstrumentCode: r.InstrumentCode,
			InstrumentName: instrumentNames[r.InstrumentCode],
			TradeDate:      r.TradeDate.Format("2006-01-02"),
			MaturityDate:   r.MaturityDate.Format("2006-01-02"),
			HoldingDays:    int32(r.HoldingDays),
			PrincipalCny:   r.PrincipalCny,
			ActualRatePct:  r.ActualRatePct,
			Status:         r.Status,
			EntrySource:    r.EntrySource,
		}
		if r.TheoreticalRatePct != nil {
			row.TheoreticalRatePct = r.TheoreticalRatePct
		}
		if r.TheoreticalReturnByIdle != nil {
			row.TheoreticalReturnByIdle = r.TheoreticalReturnByIdle
		}
		if r.TheoreticalReturnCny != nil {
			row.TheoreticalReturnCny = r.TheoreticalReturnCny
		}
		if r.IdleCnyAtRecord != nil {
			row.IdleCnyAtRecord = r.IdleCnyAtRecord
		}
		if r.ActualReturnCny != nil {
			row.ActualReturnCny = r.ActualReturnCny
		}
		if r.SettledAt != nil {
			s := r.SettledAt.Format("2006-01-02 15:04")
			row.SettledAt = &s
		}
		if r.DeviationRateBps != nil {
			row.DeviationRateBps = r.DeviationRateBps
		}
		if r.DeviationPrincipalCny != nil {
			row.DeviationPrincipalCny = r.DeviationPrincipalCny
		}
		if r.DeviationReturnCny != nil {
			row.DeviationReturnCny = r.DeviationReturnCny
		}
		if r.DeviationReturnPct != nil {
			row.DeviationReturnPct = r.DeviationReturnPct
		}
		// D-276 fee_cny + net_theoretical_return_cny（后端派生，net 非 DB 列）
		if r.FeeCny != nil {
			row.FeeCny = r.FeeCny
		}
		if r.TheoreticalReturnCny != nil && r.FeeCny != nil {
			net := *r.TheoreticalReturnCny - *r.FeeCny
			row.NetTheoreticalReturnCny = &net
		}
		if r.Rationale != nil {
			row.Rationale = *r.Rationale
		}
		resp.Trades = append(resp.Trades, row)
	}
	return connect.NewResponse(resp), nil
}

// GetRepoStrategyValidation 策略验证总览（累计理论 vs 实际偏差分析）。
func (s *Service) GetRepoStrategyValidation(ctx context.Context, req *connect.Request[v1.GetRepoStrategyValidationRequest]) (*connect.Response[v1.GetRepoStrategyValidationResponse], error) {
	v, err := s.store.GetRepoStrategyValidation(ctx)
	if err != nil {
		return nil, fmt.Errorf("get repo strategy validation: %w", err)
	}
	return connect.NewResponse(&v1.GetRepoStrategyValidationResponse{
		TotalTrades:                  v.TotalTrades,
		SettledTrades:                v.SettledTrades,
		OpenTrades:                   v.OpenTrades,
		TotalPrincipalCny:            v.TotalPrincipalCny,
		TotalTheoreticalReturnCny:    v.TotalTheoreticalReturn,
		TotalActualReturnCny:         v.TotalActualReturn,
		TotalDeviationCny:            v.TotalDeviationCny,
		TotalDeviationPct:            v.TotalDeviationPct,
		AvgRateDeviationBps:          v.AvgRateDeviationBps,
		AvgPrincipalDeviationCny:     v.AvgPrincipalDeviationCny,
		OpenPrincipalCny:             v.OpenPrincipalCny,
		OpenTheoreticalReturnCny:     v.OpenTheoreticalReturn,
		TotalFeeCny:                  v.TotalFeeCny,               // D-276
		TotalNetTheoreticalReturnCny: v.TotalNetTheoreticalReturn, // D-276
	}), nil
}
