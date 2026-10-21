// service_finance_overview.go D-348：GetFinanceOverview 聚合 RPC——三个数（入金/实盘权益/净盈亏）
// +收益分解（环境分层/收入构成）+统一流水（三源防漏计防双计）。后端算，前端零计算（D-043）。
package dashboard

import (
	"context"
	"log"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// buildFinanceOverview 全量聚合（缓存未命中时执行）。五读零依赖全并发
// （并行化票——NAV+perf+池三段同法）；pnls/incomes/flows 三核心读败仍
// 整包 500 契约不动，equity/perf 为既有如实缺面；每段耗时 log 验证面。
func (s *Service) buildFinanceOverview(ctx context.Context) (*v1.GetFinanceOverviewResponse, error) {
	var (
		pnls        []pgstore.FinancePnlEnv
		incomes     []pgstore.FinanceIncomeKind
		flows       []pgstore.FinanceFlow
		equity      float64
		byCcy       map[string]float64
		skipped     int
		equityAvail bool
		navPools    []pgstore.ExplorerIdlePool
		perf        *pgstore.PerfReport
	)
	var g errgroup.Group
	var pnlsErr, incomesErr, flowsErr error
	g.Go(func() error {
		overviewSeg("fin_pnl_env", func() { pnls, pnlsErr = s.store.FinancePnlByEnv(ctx) })
		return nil
	})
	g.Go(func() error {
		overviewSeg("fin_income_kind", func() { incomes, incomesErr = s.store.FinanceIncomeByKind(ctx) })
		return nil
	})
	g.Go(func() error {
		overviewSeg("fin_flows", func() { flows, flowsErr = s.store.FinanceFlows(ctx, financeFlowsLimit) })
		return nil
	})
	g.Go(func() error {
		overviewSeg("fin_equity_nav", func() {
			equity, skipped, byCcy, navPools, equityAvail = s.equityTotalUSD(ctx)
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("fin_perf", func() {
			if p, err := s.perfSinceBaseline(ctx); err == nil {
				perf = p
			} else {
				log.Printf("[finance] perf baseline: %v（业绩面如实缺）", err)
			}
		})
		return nil
	})
	_ = g.Wait()
	for _, e := range []error{pnlsErr, incomesErr, flowsErr} {
		if e != nil {
			return nil, e
		}
	}
	if skipped > 0 {
		log.Printf("[finance] overview: %d 个非 USD/USDT 计价账户跳过未计权益（诚实口径，不做假换算）", skipped)
	}

	resp := &v1.GetFinanceOverviewResponse{
		TotalEquityUsd:  equity,
		EquityByCcy:     byCcy,       // D-522④：投资人页 NAV 分币种双行
		EquityAvailable: equityAvail, // 读败/未接线=false——前端显「—」非 $0.00（断供≠零）
	}
	// 审计②：NAV 族陈值标注——池含冻结快照行时计数+金额+清单透传前端。
	if sn, su, sd := navStaleNote(navPools, time.Now()); sn > 0 {
		resp.NavStalePools, resp.NavStaleUsd, resp.NavStaleDetail = sn, su, sd
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
	// D-535：业绩基线面——本金池变动−外部净流；锚缺/读败 → available=false 如实缺（不冒充 0）。
	if perf != nil {
		resp.PerfAvailable = true
		resp.BaselineUsd = perf.BaselineUSD
		resp.BaselineAt = timestamppb.New(perf.BaselineAt)
		resp.PrincipalNowUsd = perf.PrincipalNowUSD
		resp.NetExternalFlowUsd = perf.NetExternalUSD
		resp.PerfUsd = perf.PerfUSD
		resp.PerfAnnPct = perf.PerfAnnPct
		resp.PerfFlowProvisional = perf.FlowProvisional
	}
	return resp, nil
}

// perfSinceBaseline 业绩基线读面（store 未接线 → nil,nil 如实缺；读败上抛由调用方标缺）。
func (s *Service) perfSinceBaseline(ctx context.Context) (*pgstore.PerfReport, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.PerfSinceBaseline(ctx, time.Now())
}

// equityTotalUSD —— D-264 M4.4②：改读 pgstore.ComputeNav 单函数（NAV 口径单源，
// deptreport/fin_nav_daily/配置器同径）。快照表写口已过滤 real-only（D-006 径——
// cex=!Simulated / cfd=venue∈is_real 键集），本层不重复筛；非 USD 池跳过+计数透传。
func (s *Service) equityTotalUSD(ctx context.Context) (total float64, skippedNonUSD int, byCcy map[string]float64, pools []pgstore.ExplorerIdlePool, available bool) {
	fn := s.computeNavFn
	if fn == nil {
		if s.store == nil {
			return 0, 0, nil, nil, false // 未接线（测试）→ 如实缺（断供≠零）
		}
		fn = s.store.ComputeNav
	}
	nav, err := fn(ctx, time.Now())
	if err != nil {
		log.Printf("[finance] overview: nav 读取失败，权益显缺不假数（available=false）: %v", err)
		return 0, 0, nil, nil, false
	}
	byCcy = map[string]float64{}
	for c, b := range nav.ByCcy {
		byCcy[c] = b.Nav
	}
	// D-630 返修⑤：总权益=USDT+USD 两桶合（本金口径——原 NavUSD=USD 桶
	// 单读致 earn USDT 1592.43 漏计=10777 错值实证；非 U 币桶 ByCcy 可见不入）。
	total = nav.ByCcy["USD"].Nav + nav.ByCcy["USDT"].Nav
	return total, nav.SkippedNonUSD, byCcy, nav.Pools, true
}
