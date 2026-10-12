// service_finance_overview.go D-348：GetFinanceOverview 聚合 RPC——三个数（入金/实盘权益/净盈亏）
// +收益分解（环境分层/收入构成）+统一流水（三源防漏计防双计）。后端算，前端零计算（D-043）。
package dashboard

import (
	"context"
	"log"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"nox/internal/data/keydomain"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
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
	equity, skippedNonUSD := s.equityTotalUSD(ctx)
	if skippedNonUSD > 0 {
		log.Printf("[finance] overview: %d 个非 USD 计价账户跳过未计权益（诚实口径，不做假换算）", skippedNonUSD)
	}

	resp := &v1.GetFinanceOverviewResponse{
		TotalEquityUsd: equity,
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

// equityTotalUSD Σ实盘权益（在线或快照——AccountMonitor 离线保留最近成功快照，同计入）。
// 只计实盘：CFD=broker_accounts.is_real 三态（D-144：仅 &true 计入；nil 未知/false demo 不计）
// 且 status≠stopped；CEX=simulated=false。AccountInfo 本体无 is_real（CFD 零值无意义）——
// 经 venue_key 前缀 "{venue}:{type}" 接 broker_accounts 映射。非 USD 计价跳过+计数（诚实口径不假换算）。
func (s *Service) equityTotalUSD(ctx context.Context) (total float64, skippedNonUSD int) {
	if s.acctMon == nil {
		return 0, 0 // 未接线（测试）→ 诚实零值
	}
	accounts := s.acctMon.Snapshot()
	if len(accounts) == 0 {
		return 0, 0
	}
	brokers, err := s.store.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[finance] overview: broker_accounts 读取失败，CFD 权益不计: %v", err)
		brokers = nil // CEX 侧仍可计（fail-partial，不因 CFD 元数据失败丢弃全部口径）
	}
	byVenue := make(map[string]pgstore.BrokerAccount, len(brokers))
	for _, b := range brokers {
		byVenue[b.Venue] = b
	}
	for _, a := range accounts {
		if a.Currency != "USD" {
			skippedNonUSD++
			continue
		}
		if a.AssetType == "cex" {
			if !a.Simulated {
				total += a.Equity
			}
			continue
		}
		venue := keydomain.BareOf(a.VenueKey)
		b, ok := byVenue[venue]
		if !ok || b.IsReal == nil || !*b.IsReal || b.Status == "stopped" {
			continue // nil=未知（未知≠实盘）/demo/stopped 一律不计
		}
		total += a.Equity
	}
	return total, skippedNonUSD
}
