// service_overview_bundle.go —— D-536 总览页聚合（一次返回五段；前端零运算只渲染）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// GetOverviewBundle 五段：§0 待办 → §1 本金分档 → §2 业绩 → §3 订单 → §4 风险心跳。
// 缺数据显缺不显 0（单面读败只影响该面，其他面照常——总览面 fail-open 观测径）。
func (s *Service) GetOverviewBundle(ctx context.Context, _ *connect.Request[v1.GetOverviewBundleRequest]) (*connect.Response[v1.GetOverviewBundleResponse], error) {
	resp := &v1.GetOverviewBundleResponse{}
	now := time.Now()
	// §0 待办请求箱（requested 态行数）。
	if rows, err := s.store.ListRequestedGaps(ctx); err == nil {
		resp.PendingRequests = int32(len(rows))
	}
	// §1 本金分档（USDT+USD 两桶——D-534；非 U 币池不进本金面）。
	if nav, err := s.store.ComputeNav(ctx, now); err == nil {
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
	// 锁定档=在仓单披露子行（不进本金总计——D-543R1 实证 17,199 双计：fin_dci_order
	// 的 filled 行含 earn 申购单（cash_mgmt/earn_apr_lending/结构化），其本金已在
	// venue earn 快照桶内，再叠加必双计）。若某产品本金不在任何快照桶=快照覆盖缺口，
	// 该修快照写径而非订单加总。最早回笼=最小到期（信息面）。
	locked := 0.0
	var earliest *time.Time
	if orders, err := s.store.ListDCIOrders(ctx, 100); err == nil {
		for _, o := range orders {
			if o.Status != "filled" {
				continue
			}
			locked += o.Qty
			if o.TenorDays != nil {
				t := o.CreatedAt.Add(time.Duration(*o.TenorDays) * 24 * time.Hour)
				if earliest == nil || t.Before(*earliest) {
					earliest = &t
				}
			}
			resp.Orders = append(resp.Orders, &v1.OverviewOrderRow{
				Kind:      "dci",
				Label:     fmt.Sprintf("%s %s #%d", o.Venue, o.ProductRef, o.ID),
				AmountUsd: o.Qty,
				Status:    o.Status,
				Until:     timestamppb.New(o.CreatedAt.Add(time.Duration(derefInt(o.TenorDays)) * 24 * time.Hour)),
			})
		}
	}
	resp.PrincipalLockedUsd = locked // 披露值：在息面中锁定子档，非第三桶钱
	if earliest != nil {
		resp.EarliestRelease = timestamppb.New(*earliest)
	}
	resp.PrincipalTotalUsd = resp.PrincipalIdleUsd + resp.PrincipalEarningUsd // 快照池即账本真值
	// §5 十段工序健康读模（S3′-b：后端聚合 emit 时戳+wait_reason+错误面——
	// 禁前端拿 dept 拼状态/禁前端判故障）。
	resp.Stages = s.pipelineStages(ctx)
	// §2 业绩（D-535 锚面——缺锚/读败 available=false 如实缺）。
	if perf, err := s.store.PerfSinceBaseline(ctx, now); err == nil && perf != nil {
		resp.PerfAvailable = true
		resp.BaselineUsd = perf.BaselineUSD
		resp.BaselineAt = timestamppb.New(perf.BaselineAt)
		resp.PerfUsd = perf.PerfUSD
		resp.PerfAnnPct = perf.PerfAnnPct
		resp.PerfFlowProvisional = perf.FlowProvisional
	}
	// §3 autotrade 在仓行+纸面计数（标沙箱）。
	if poss, err := s.store.ListPositions(ctx, "ACTIVE", 100, 0); err == nil {
		for _, p := range poss {
			row := &v1.OverviewOrderRow{
				Kind:   "autotrade",
				Label:  fmt.Sprintf("#%d %s", p.ID, p.Type),
				Status: p.Status,
			}
			if p.NotionalUSD != nil {
				row.AmountUsd = *p.NotionalUSD
			}
			if p.OpenedAt != nil {
				row.Until = timestamppb.New(*p.OpenedAt)
			}
			resp.Orders = append(resp.Orders, row)
		}
	}
	if papers, err := s.store.ListActivePaperSpotCarry(ctx); err == nil {
		resp.PaperCount = int32(len(papers))
	}
	// §6 收益归因分解（产出面补建——策略键×30d 结算净额，后端聚合）。
	if attrib, err := s.store.OverviewAttribution(ctx, now.Add(-30*24*time.Hour)); err == nil {
		for _, a := range attrib {
			resp.Attribution = append(resp.Attribution, &v1.OverviewAttribRow{
				Strategy: a.Strategy, Positions: int32(a.Positions),
				Net30Usd: a.Net30USD, Wins: int32(a.Wins), Losses: int32(a.Losses),
			})
		}
	}
	// §7 闭环漏斗计数（八环 48h——发现→评估→沙箱→执行→持仓→平仓→账本→复盘→淘汰）。
	if funnel, err := s.store.OverviewFunnel(ctx, now.Add(-48*time.Hour)); err == nil {
		for _, f := range funnel {
			resp.Funnel = append(resp.Funnel, &v1.OverviewFunnelRow{
				Stage: f.Stage, Label: f.Label, Count_48H: int32(f.Count48h), Cumulative: f.Cumulative,
			})
		}
	}
	// §8 等待面缩览（pending/unqualified 毕业+proposed 提案——等待因+龄）。
	s.appendOverviewWaiting(ctx, resp, now)
	// §9 眠钱时长（idle_floor_streak 最长连破天数——D-286 眠钱不眠计量呈现面）。
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
	// §4 风险心跳（kill 态+30d 净额对硬线+24h critical 数）。
	if gc := s.gateChecker; gc != nil {
		_, allowed, _, err := gc.CheckGate(ctx)
		resp.KillSwitch = err == nil && !allowed
	}
	if n30, err := s.store.NetRealizedSince(ctx, now.Add(-30*24*time.Hour), false); err == nil {
		resp.Net30Usd = n30
	}
	if alerts, err := s.store.ListAlerts(ctx, 500, 0); err == nil {
		cut := now.Add(-24 * time.Hour)
		for _, a := range alerts {
			if a.Level == "critical" && a.Ts.After(cut) {
				resp.CritAlerts_24H++
			}
		}
	}
	return connect.NewResponse(resp), nil
}

// appendOverviewWaiting §8 等待面缩览：pending/unqualified 毕业行+proposed 提案行
// （等待因=现状关键值，龄=since 起点；各面上限 5 行防淹没）。
func (s *Service) appendOverviewWaiting(ctx context.Context, resp *v1.GetOverviewBundleResponse, now time.Time) {
	const perKind = 5
	if grads, err := filterGraduations(ctx, s, "pending", perKind); err == nil {
		for _, g := range grads {
			resp.Waiting = append(resp.Waiting, &v1.OverviewWaitingRow{
				Kind:      "graduation_pending",
				Label:     fmt.Sprintf("毕业#%d %s %s", g.ID, g.Symbol, g.Venue),
				Detail:    fmt.Sprintf("待批·资金费年化 %s", floatOrDash(g.LastFundingAnn, "%.2f%%")),
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

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
