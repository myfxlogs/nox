// service_intra.go D-441 S5：日内纸面轨（沙箱·D-425）观测面双 RPC。
// ListIntraTrades=成交列表（baseline 过滤+limit 夹取）；IntraTrackStats=30 天裁决统计
// （baseline 分组聚合全后端算+步进心跳+观测天数/门距）。前端零信任只渲染。
package dashboard

import (
	"context"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/scan"
)

const intraTrackGateDays = 30 // D-425 SSOT：30 天观测裁决门

// ListIntraTrades 成交列表（id DESC；limit 夹取 [1,1000]，缺省 200）。
func (s *Service) ListIntraTrades(ctx context.Context, req *connect.Request[v1.ListIntraTradesRequest]) (*connect.Response[v1.ListIntraTradesResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	trades, err := s.store.ListIntraTrades(ctx, req.Msg.Baseline, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	rows := make([]*v1.IntraTradeRow, 0, len(trades))
	for _, t := range trades {
		r := &v1.IntraTradeRow{
			Id: t.ID, Baseline: t.Baseline, Symbol: t.Symbol, Venue: t.Venue,
			Side: t.Side, Status: t.Status, EntryTs: t.EntryTs.Unix(), EntryPx: t.EntryPx,
			Qty: t.Qty, NotionalUsd: t.NotionalUSD, MarginUsd: t.MarginUSD,
			StopPx: t.StopPx, ExitReason: t.ExitReason,
		}
		if t.ExitTs != nil {
			r.ExitTs = t.ExitTs.Unix()
		}
		if t.ExitPx != nil {
			r.ExitPx = *t.ExitPx
		}
		r.SlippageBps = t.SlippageBps
		r.PnlUsd = t.PnlUSD
		rows = append(rows, r)
	}
	return connect.NewResponse(&v1.ListIntraTradesResponse{Trades: rows}), nil
}

// IntraTrackStats 30 天裁决统计（baseline 聚合全 SQL 算；days_elapsed 含首日=1）。
func (s *Service) IntraTrackStats(ctx context.Context, _ *connect.Request[v1.IntraTrackStatsRequest]) (*connect.Response[v1.IntraTrackStatsResponse], error) {
	stats, err := s.store.IntraTrackStatsQuery(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	days, err := s.store.IntraDaysElapsed(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &v1.IntraTrackStatsResponse{DaysElapsed: int32(days)}
	if last := scan.IntraLastStepAt(); !last.IsZero() {
		resp.LastStepUnix = last.Unix()
	}
	if toGate := intraTrackGateDays - days; toGate > 0 {
		resp.DaysToGate = int32(toGate)
	}
	resp.Baselines = make([]*v1.IntraBaselineStat, 0, len(stats))
	for _, r := range stats {
		resp.Baselines = append(resp.Baselines, &v1.IntraBaselineStat{
			Baseline: r.Baseline, Total: int32(r.Total), Open: int32(r.Open),
			Closed: int32(r.Closed), PnlUsd: r.PnlUSD, WinCount: int32(r.WinCount),
			SlipP90Bps: r.SlipP90Bps, CircuitDays: int32(r.CircuitDays),
			KillCount: int32(r.KillCount),
		})
	}
	return connect.NewResponse(resp), nil
}
