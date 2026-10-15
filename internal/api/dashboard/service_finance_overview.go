// service_finance_overview.go D-348：GetFinanceOverview 聚合 RPC——三个数（入金/实盘权益/净盈亏）
// +收益分解（环境分层/收入构成）+统一流水（三源防漏计防双计）。后端算，前端零计算（D-043）。
package dashboard

import (
	"context"
	"log"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
)

// financeOverviewTTL 聚合缓存 TTL（D-331 ActiveOpportunityIDs 15s 先例——聚合查询防热路径重压）。
const financeOverviewTTL = 15 * time.Second

// financeFlowsLimit 统一流水行数上限（任务书 §S1：LIMIT 200 滚动）。
const financeFlowsLimit = 200

// GetFinanceOverview 财务总览聚合（15s TTL 缓存；缓存按响应整体，无失效钩子——
// 15s 陈旧窗对财务总览可接受，同 D-331 语义）。
func (s *Service) GetFinanceOverview(ctx context.Context, req *connect.Request[v1.GetFinanceOverviewRequest]) (*connect.Response[v1.GetFinanceOverviewResponse], error) {
	s.financeMu.Lock()
	if s.financeCached != nil && time.Since(s.financeCachedAt) < financeOverviewTTL {
		resp := s.financeCached
		s.financeMu.Unlock()
		return connect.NewResponse(resp), nil
	}
	s.financeMu.Unlock()

	resp, err := s.buildFinanceOverview(ctx)
	if err != nil {
		return nil, err
	}
	s.financeMu.Lock()
	s.financeCached, s.financeCachedAt = resp, time.Now()
	s.financeMu.Unlock()
	return connect.NewResponse(resp), nil
}

// buildFinanceOverview 全量聚合（缓存未命中时执行）。
func (s *Service) buildFinanceOverview(ctx context.Context) (*v1.GetFinanceOverviewResponse, error) {
	pnls, err := s.store.FinancePnlByEnv(ctx)
	if err != nil {
		return nil, err
	}
	incomes, err := s.store.FinanceIncomeByKind(ctx)
	if err != nil {
		return nil, err
	}
	flows, err := s.store.FinanceFlows(ctx, financeFlowsLimit)
	if err != nil {
		return nil, err
	}
	equity, skippedNonUSD, byCcy := s.equityTotalUSD(ctx)
	if skippedNonUSD > 0 {
		log.Printf("[finance] overview: %d 个非 USD/USDT 计价账户跳过未计权益（诚实口径，不做假换算）", skippedNonUSD)
	}

	resp := &v1.GetFinanceOverviewResponse{
		TotalEquityUsd: equity,
		EquityByCcy:    byCcy, // D-522④：投资人页 NAV 分币种双行
	}
	var totalRealized float64
	for _, r := range pnls {
		totalRealized += r.RealizedPnl // D-508 S8：前端零聚合
		resp.PnlByEnv = append(resp.PnlByEnv, &v1.FinancePnlByEnv{
			Environment: r.Environment, RealizedPnl: r.RealizedPnl, Closed: int32(r.Closed),
		})
	}
	resp.TotalRealizedUsd = totalRealized
	for _, r := range incomes {
		resp.IncomeByKind = append(resp.IncomeByKind, &v1.FinanceIncomeRow{
			Kind: r.Kind, TotalUsd: r.TotalUSD, Rows: int32(r.Rows),
		})
	}
	for _, f := range flows {
		row := &v1.FinanceFlowRow{
			Source: f.Source, Kind: f.Kind, Symbol: f.Symbol,
			AmountUsd: f.AmountUSD, PositionId: f.PositionID, Note: f.Note,
		}
		if !f.Ts.IsZero() {
			row.Ts = timestamppb.New(f.Ts)
		}
		resp.Flows = append(resp.Flows, row)
	}
	return resp, nil
}

// equityTotalUSD —— D-264 M4.4②：改读 pgstore.ComputeNav 单函数（NAV 口径单源，
// deptreport/fin_nav_daily/配置器同径）。快照表写口已过滤 real-only（D-006 径——
// cex=!Simulated / cfd=venue∈is_real 键集），本层不重复筛；非 USD 池跳过+计数透传。
func (s *Service) equityTotalUSD(ctx context.Context) (total float64, skippedNonUSD int, byCcy map[string]float64) {
	if s.store == nil {
		return 0, 0, nil // 未接线（测试）→ 诚实零值
	}
	nav, err := s.store.ComputeNav(ctx, time.Now())
	if err != nil {
		log.Printf("[finance] overview: nav 读取失败，权益按零值报（诚实口径不假数）: %v", err)
		return 0, 0, nil
	}
	byCcy = map[string]float64{}
	for c, b := range nav.ByCcy {
		byCcy[c] = b.Nav
	}
	return nav.NavUSD, nav.SkippedNonUSD, byCcy
}
