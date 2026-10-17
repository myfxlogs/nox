// service_overview_output.go —— 总览产出面装配（§6-§10：组成明细下钻+归因+漏斗+等待面+眠钱）。
// 棘轮拆分自 service_overview_bundle.go（300 行基线）；后端拆好前端直渲，零运算。
package dashboard

import (
	"context"
	"fmt"
	"nox/internal/data/fact"
	"sort"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// appendOverviewOutput §6-§10 产出面一次装配——组成明细（六组）+收益归因+闭环漏斗+
// 等待面+眠钱时长。pools=§1 ComputeNav 池行（nil=读败面如实空）；perf=§2 业绩报告
// （nil=无锚/读败，invested 面随缺）。
func (s *Service) appendOverviewOutput(ctx context.Context, resp *v1.GetOverviewBundleResponse, pools []pgstore.ExplorerIdlePool, perf *pgstore.PerfReport, now time.Time) {
	s.appendOverviewBreakdowns(resp, pools)
	s.appendOverviewInvested(ctx, resp, perf)
	// 收益归因（策略键×30d 结算净额）。
	if attrib, err := s.store.OverviewAttribution(ctx, now.Add(-30*24*time.Hour)); err == nil {
		for _, a := range attrib {
			resp.Attribution = append(resp.Attribution, &v1.OverviewAttribRow{
				Strategy: a.Strategy, Positions: int32(a.Positions),
				Net30Usd: a.Net30USD, Wins: int32(a.Wins), Losses: int32(a.Losses),
			})
		}
	}
	// 闭环漏斗（九环 48h——发现→评估→沙箱→执行→持仓→平仓→账本→复盘→淘汰）。
	if funnel, err := s.store.OverviewFunnel(ctx, now.Add(-48*time.Hour)); err == nil {
		for _, f := range funnel {
			resp.Funnel = append(resp.Funnel, &v1.OverviewFunnelRow{
				Stage: f.Stage, Label: f.Label, Count_48H: int32(f.Count48h), Cumulative: f.Cumulative,
			})
		}
	}
	s.appendOverviewWaiting(ctx, resp)
	// 眠钱时长（idle_floor_streak 最长连破天数——D-286 眠钱不眠计量呈现面）。
	if streaks, err := s.store.GetStreaks(ctx); err == nil {
		bestDays, bestPool := 0, ""
		for k, st := range streaks {
			if st.Days > bestDays {
				bestDays, bestPool = st.Days, k
			}
		}
		resp.IdleStreakDays = int32(bestDays)
		resp.IdleStreakPool = bestPool
	}
}

// appendOverviewBreakdowns 池行→四组组成明细：nav=venue 级 USD 族加总+非USD币种计数
// （池=venue×symbol 粒度，114 行灰尘币不可读实证修——跨币永不加总）；idle/earning=
// U 币池逐行；principal=每 venue 对本金的贡献（earn 池记 equity、余记 balance——
// 与 PrincipalTotalUsd 同口径的逐项分解，和卡值对得上）。
func (s *Service) appendOverviewBreakdowns(resp *v1.GetOverviewBundleResponse, pools []pgstore.ExplorerIdlePool) {
	usdEq := map[string]float64{}
	altN := map[string]int{}
	var vOrder []string
	for _, p := range pools {
		if p.Ghost {
			continue
		}
		if _, seen := usdEq[p.Venue]; !seen {
			usdEq[p.Venue] = 0
			vOrder = append(vOrder, p.Venue)
		}
		if p.Currency != "USDT" && p.Currency != "USD" {
			altN[p.Venue]++
			continue
		}
		usdEq[p.Venue] += p.Equity
	}
	for _, v := range vOrder {
		sub := ""
		if n := altN[v]; n > 0 {
			sub = fmt.Sprintf("另含 %d 种非USD资产（原量分列见财务页）", n)
		}
		resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
			Group: "nav", Label: v, Ccy: "USD", Amount: usdEq[v], Sub: sub,
		})
	}
	acc := map[string]*v1.OverviewBreakdownRow{}
	var order []string
	for _, p := range pools {
		if p.Ghost || (p.Currency != "USDT" && p.Currency != "USD") {
			continue
		}
		if p.VenueType == "earn" {
			resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
				Group: "earning", Label: p.Venue, Ccy: p.Currency, Amount: p.Equity, Sub: p.VenueType,
			})
		} else {
			resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
				Group: "idle", Label: p.Venue, Ccy: p.Currency, Amount: p.Balance, Sub: p.VenueType,
			})
		}
		r, ok := acc[p.Venue]
		if !ok {
			r = &v1.OverviewBreakdownRow{Group: "principal", Label: p.Venue, Ccy: "USD", Sub: p.VenueType}
			acc[p.Venue] = r
			order = append(order, p.Venue)
		}
		if p.VenueType == "earn" {
			r.Amount += p.Equity
		} else {
			r.Amount += p.Balance
		}
	}
	for _, v := range order {
		resp.Breakdowns = append(resp.Breakdowns, acc[v])
	}
}

// appendOverviewInvested 总投入+业绩分解——invested 组=基线锚+按 venue 拆的基线后净
// 充提（投资人累计投入=基线+净充提，构成项与卡值对得上）；perf 组=基线→现值→外部
// 净流→真业绩分解行。perf nil=无锚/读败，整面随缺（显缺不显 0）。
func (s *Service) appendOverviewInvested(ctx context.Context, resp *v1.GetOverviewBundleResponse, perf *pgstore.PerfReport) {
	if perf == nil {
		return
	}
	resp.InvestedAvailable = true
	resp.NetInvestedUsd = perf.BaselineUSD + perf.NetExternalUSD
	row := func(g, label, sub string, amt float64) {
		resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
			Group: g, Label: label, Ccy: "USD", Amount: amt, Sub: sub,
		})
	}
	row("perf", "基线锚", perf.BaselineAt.Format("2006-01-02"), perf.BaselineUSD)
	row("perf", "本金池现值", "USDT+USD 桶", perf.PrincipalNowUSD)
	row("perf", "外部净流（基线后）", "充正提负", perf.NetExternalUSD)
	row("perf", "真业绩", "现值−基线−净流", perf.PerfUSD)
	row("invested", "期初本金（基线锚）", perf.BaselineAt.Format("2006-01-02"), perf.BaselineUSD)
	if byV, err := s.store.NetExternalFlowByVenueSince(ctx, perf.BaselineAt); err == nil {
		vs := make([]string, 0, len(byV))
		for v := range byV {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		for _, v := range vs {
			row("invested", v, "基线后净充提", byV[v])
		}
	}
}

// appendOverviewWaiting 等待面缩览：pending/unqualified 毕业行+proposed 提案行
// （等待因=现状关键值，龄=since 起点；各面上限 5 行防淹没）。
// overviewFundingFresh 待批卡 funding facts 龄期阈（nogate-3 残余注记 P2 票④）：
// funding 8h 周期×3 窗——超龄行视作陈值不采用（展示面如实缺）。
const overviewFundingFresh = 24 * time.Hour

func (s *Service) appendOverviewWaiting(ctx context.Context, resp *v1.GetOverviewBundleResponse) {
	now := time.Now()
	const perKind = 5
	if grads, err := filterGraduations(ctx, s, "pending", perKind); err == nil {
		for _, g := range grads {
			// D-604 顺手件：funding_ann 改读 facts 现值（快照列脱钩——展示面同型如实缺）。
			// P2 票④龄期标（nogate-3 残余注记）：行龄>24h（funding 8h×3 窗）视作
			// 陈值不采用——展示面如实缺（禁陈值当现值渲染）。
			var liveAnn *float64
			if frows, ferr := s.store.LatestFacts(ctx, fact.KindFunding, g.Venue, g.Symbol); ferr == nil && len(frows) > 0 {
				if now.Sub(frows[0].Ts) <= overviewFundingFresh {
					liveAnn = &frows[0].Value
				}
			}
			resp.Waiting = append(resp.Waiting, &v1.OverviewWaitingRow{
				Kind:      "graduation_pending",
				Label:     fmt.Sprintf("毕业#%d %s %s", g.ID, g.Symbol, g.Venue),
				Detail:    fmt.Sprintf("待批·资金费年化 %s", floatOrDash(liveAnn, "%.2f%%")),
				SinceUnix: g.CreatedAt.Unix(),
			})
		}
	}
	if grads, err := filterGraduations(ctx, s, "unqualified", perKind); err == nil {
		for _, g := range grads {
			resp.Waiting = append(resp.Waiting, &v1.OverviewWaitingRow{
				Kind:      "graduation_watch",
				Label:     fmt.Sprintf("观察#%d %s %s", g.ID, g.Symbol, g.Venue),
				Detail:    fmt.Sprintf("摘牌待复活·%s", truncateRunes(g.DecidedNote, 40)),
				SinceUnix: g.CreatedAt.Unix(),
			})
		}
	}
	if props, err := s.store.ListProposals(ctx, "proposed", perKind); err == nil {
		for _, pr := range props {
			apr := "—"
			if pr.ExpectedAPR != nil {
				apr = fmt.Sprintf("%.2f%%", *pr.ExpectedAPR*100)
			}
			resp.Waiting = append(resp.Waiting, &v1.OverviewWaitingRow{
				Kind:      "proposal",
				Label:     fmt.Sprintf("提案#%d %s@%s", pr.ID, pr.TemplateID, pr.Venue),
				Detail:    fmt.Sprintf("待执行·APR %s", apr),
				SinceUnix: pr.CreatedAt.Unix(),
			})
		}
	}
}

// filterGraduations 按态取前 N（ListCarryGraduations 全量读+内存过滤——表行数百级，
// 专用读口不值得；per-kind 限流在缩览面折断）。
func filterGraduations(ctx context.Context, s *Service, status string, limit int) ([]pgstore.CarryGraduation, error) {
	all, err := s.store.ListCarryGraduations(ctx)
	if err != nil {
		return nil, err
	}
	var out []pgstore.CarryGraduation
	for _, g := range all {
		if g.Status == status {
			out = append(out, g)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func floatOrDash(v *float64, format string) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf(format, *v)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
