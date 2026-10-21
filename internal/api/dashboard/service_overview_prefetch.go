// service_overview_prefetch.go —— 总览 bundle 预取层（性能票①：五段 errgroup
// 并发——各段失败域本就各自容灾，err 吞掉保「读败如实缺」语义；装配序=
// 原串行序不变由调用方保）。棘轮拆件。
// D-626 后并行化续票：§3 五读拆各自 goroutine（彼此零依赖）；attribution/
// funnel/waiting/streaks 四段只依赖 store 不吃 pf——从 appendOverviewOutput
// 上提进本层并发面，不再被 Wait 阻塞在队尾。每段耗时 log（部署后 SG 实拍
// 各段真实耗时的验证面）。
package dashboard

import (
	"context"
	"log"
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

// overviewSeg 段耗时 log 包装（各段实测耗时=性能验证面）。
func overviewSeg(name string, f func()) {
	t0 := time.Now()
	f()
	log.Printf("[overview] seg %s: %s", name, time.Since(t0).Round(time.Millisecond))
}

// prefetchOverview 全独立读并发（resp 内直填段=PendingRequests/本金两档/
// Stages/KillSwitch/Attribution/Funnel/Waiting/IdleStreak——字段互不重叠
// 且每字段仅单写者，无竞态）。依赖 pf 的段（breakdowns 吃 navPools、
// invested 吃 perf.BaselineAt）不进本层——留 Wait 后装配，串行原因在案。
func (s *Service) prefetchOverview(ctx context.Context, now time.Time, resp *v1.GetOverviewBundleResponse) overviewPrefetch {
	var pf overviewPrefetch
	var g errgroup.Group
	// §0 待办请求箱（requested 态行数）。
	g.Go(func() error {
		overviewSeg("pending_gaps", func() {
			if rows, err := s.store.ListRequestedGaps(ctx); err == nil {
				resp.PendingRequests = int32(len(rows))
			}
		})
		return nil
	})
	// §1 本金分档（USDT+USD 两桶——D-534；非 U 币池不进本金面）。
	// 池行留手喂 §6-§10 产出面。
	g.Go(func() error {
		overviewSeg("nav", func() {
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
				resp.PrincipalAvailable = true // F1：读成才真（读败=缺省 false→前端显「—」不冒 $0）
			}
		})
		return nil
	})
	// §orders/§2 业绩/§3 仓行·纸面/§4 风险四读——各自独立失败域独立并发。
	g.Go(func() error {
		overviewSeg("dci_orders", func() {
			if o, err := s.store.ListDCIOrders(ctx, 100); err == nil {
				pf.orders = o
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("perf", func() {
			if p, err := s.store.PerfSinceBaseline(ctx, now); err == nil {
				pf.perf = p
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("positions", func() {
			if p, err := s.store.ListPositions(ctx, "ACTIVE", 100, 0); err == nil {
				pf.poss = p
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("paper_carry", func() {
			if p, err := s.store.ListActivePaperSpotCarry(ctx); err == nil {
				pf.papers = p
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("alerts", func() {
			// 级+窗下沉 SQL（原 ListAlerts(500) 全级拉取 Go 过滤——风暴期搬运浪费）。
			if a, err := s.store.ListAlertsLevelSince(ctx, "critical", now.Add(-24*time.Hour)); err == nil {
				pf.alerts = a
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("net30", func() {
			if n, err := s.store.NetRealizedSince(ctx, now.Add(-30*24*time.Hour), false); err == nil {
				pf.n30, pf.n30ok = n, true
			}
		})
		return nil
	})
	g.Go(func() error {
		overviewSeg("kill_switch", func() {
			if gc := s.gateChecker; gc != nil {
				ks, err := gc.IsKillSwitchActive(ctx)
				resp.KillSwitch = err != nil || ks
				pf.killDone = true
			}
		})
		return nil
	})
	// §5 十段工序健康读模（后端聚合 emit 时戳——禁前端判故障）。
	g.Go(func() error {
		overviewSeg("pipeline_stages", func() {
			resp.Stages = s.pipelineStages(ctx)
		})
		return nil
	})
	// §7 收益归因（策略键×30d 结算净额——零 pf 依赖，上提并发）。
	g.Go(func() error {
		overviewSeg("attribution", func() {
			if attrib, err := s.store.OverviewAttribution(ctx, now.Add(-30*24*time.Hour)); err == nil {
				for _, a := range attrib {
					resp.Attribution = append(resp.Attribution, &v1.OverviewAttribRow{
						Strategy: a.Strategy, Positions: int32(a.Positions),
						Net30Usd: a.Net30USD, Wins: int32(a.Wins), Losses: int32(a.Losses),
					})
				}
			}
		})
		return nil
	})
	// §8 闭环漏斗（九环 48h——发现→评估→沙箱→执行→持仓→平仓→账本→复盘→淘汰）。
	g.Go(func() error {
		overviewSeg("funnel", func() {
			if funnel, err := s.store.OverviewFunnel(ctx, now.Add(-48*time.Hour)); err == nil {
				for _, f := range funnel {
					resp.Funnel = append(resp.Funnel, &v1.OverviewFunnelRow{
						Stage: f.Stage, Label: f.Label, Count_48H: int32(f.Count48h), Cumulative: f.Cumulative,
					})
				}
			}
		})
		return nil
	})
	// §9 等待面缩览（零 pf 依赖）。
	g.Go(func() error {
		overviewSeg("waiting", func() {
			s.appendOverviewWaiting(ctx, resp)
		})
		return nil
	})
	// §10 眠钱时长（idle_floor_streak 最长连破天数——D-286 计量呈现面）。
	g.Go(func() error {
		overviewSeg("idle_streak", func() {
			streaks, err := s.store.GetStreaks(ctx)
			state, days, pool := summarizeIdleStreak(streaks, err)
			resp.IdleStreakState = state
			resp.IdleStreakDays = days
			resp.IdleStreakPool = pool
			resp.IdleStreakAvailable = err == nil // F4/F1：读败不冒「无在册破地板池」
		})
		return nil
	})
	// §12 D-630 真实净盈利权威口径（D-636 缓存化：每请求重算→5min 单赢家刷，
	// 池饿定位修复见 service_overview_truenet.go）。
	g.Go(func() error {
		overviewSeg("true_net", func() {
			s.trueNetSeg(resp, now)
		})
		return nil
	})
	_ = g.Wait() // 段失败域各自吞——g 恒 nil
	return pf
}

// summarizeIdleStreak 眠钱瓦片后端分态（10-21 修——前端零算零猜）：
// 读败或读模零行=NO_DATA（断供，「—」警示态）；在册零破地板池=HEALTHY_ZERO
// （健康零，「0 · 无眠钱」常志态）；有破地板池=STREAK（携最长天数+池名）。
func summarizeIdleStreak(streaks map[string]pgstore.IdleStreak, err error) (v1.IdleStreakState, int32, string) {
	if err != nil || len(streaks) == 0 {
		return v1.IdleStreakState_IDLE_STREAK_STATE_NO_DATA, 0, ""
	}
	bestDays, bestPool := 0, ""
	for k, st := range streaks {
		if st.Days > bestDays {
			bestDays, bestPool = st.Days, k
		}
	}
	if bestDays == 0 {
		return v1.IdleStreakState_IDLE_STREAK_STATE_HEALTHY_ZERO, 0, ""
	}
	return v1.IdleStreakState_IDLE_STREAK_STATE_STREAK, int32(bestDays), bestPool
}
