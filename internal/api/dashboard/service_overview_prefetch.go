// service_overview_prefetch.go —— 总览 bundle 预取层（性能票①：五段 errgroup
// 并发——各段失败域本就各自容灾，err 吞掉保「读败如实缺」语义；装配序=
// 原串行序不变由调用方保）。棘轮拆件。
package dashboard

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

type overviewPrefetch struct {
	navPools []pgstore.ExplorerIdlePool
	orders   []pgstore.DCIOrder
	perf     *pgstore.PerfReport
	poss     []pgstore.Position
	papers   []pgstore.PaperSpotCarry
	alerts   []pgstore.Alert
	n30      float64
	n30ok    bool
	killDone bool
}

// prefetchOverview 五段独立读并发（resp 内直填段=PendingRequests/本金两档/
// Stages/KillSwitch——字段互不重叠无竞态）。
func (s *Service) prefetchOverview(ctx context.Context, now time.Time, resp *v1.GetOverviewBundleResponse) overviewPrefetch {
	var pf overviewPrefetch
	var g errgroup.Group
	// §0 待办请求箱（requested 态行数）。
	g.Go(func() error {
		if rows, err := s.store.ListRequestedGaps(ctx); err == nil {
			resp.PendingRequests = int32(len(rows))
		}
		return nil
	})
	// §1 本金分档（USDT+USD 两桶——D-534；非 U 币池不进本金面）。
	// 池行留手喂 §6-§10 产出面。
	g.Go(func() error {
		if nav, err := s.store.ComputeNav(ctx, now); err == nil {
			pf.navPools = nav.Pools
			var idle, earning float64
			for _, p := range nav.Pools {
				if p.Ghost || (p.Currency != "USDT" && p.Currency != "USD") {
					continue
				}
				if p.VenueType == "earn" {
					earning += p.Equity
				} else {
					idle += p.Balance
				}
			}
			resp.PrincipalIdleUsd = idle
			resp.PrincipalEarningUsd = earning
		}
		return nil
	})
	// §orders/§2 业绩/§3 仓行/§4 风险——各自独立失败域。
	g.Go(func() error {
		if o, err := s.store.ListDCIOrders(ctx, 100); err == nil {
			pf.orders = o
		}
		return nil
	})
	g.Go(func() error {
		if p, err := s.store.PerfSinceBaseline(ctx, now); err == nil {
			pf.perf = p
		}
		return nil
	})
	g.Go(func() error {
		if p, err := s.store.ListPositions(ctx, "ACTIVE", 100, 0); err == nil {
			pf.poss = p
		}
		if p, err := s.store.ListActivePaperSpotCarry(ctx); err == nil {
			pf.papers = p
		}
		if a, err := s.store.ListAlerts(ctx, 500, 0); err == nil {
			pf.alerts = a
		}
		if n, err := s.store.NetRealizedSince(ctx, now.Add(-30*24*time.Hour), false); err == nil {
			pf.n30, pf.n30ok = n, true
		}
		if gc := s.gateChecker; gc != nil {
			ks, err := gc.IsKillSwitchActive(ctx)
			resp.KillSwitch = err != nil || ks
			pf.killDone = true
		}
		return nil
	})
	// §5 十段工序健康读模（后端聚合 emit 时戳——禁前端判故障）。
	g.Go(func() error {
		resp.Stages = s.pipelineStages(ctx)
		return nil
	})
	_ = g.Wait() // 段失败域各自吞——g 恒 nil
	return pf
}
