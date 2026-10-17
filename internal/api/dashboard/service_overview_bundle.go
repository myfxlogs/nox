// service_overview_bundle.go —— D-536 总览页聚合（一次返回五段；前端零运算只渲染）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
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
	// 同拍喂 §6 组成明细：nav 组=全池（含非 USD 原生币如实分币列）；
	// principal/idle/earning 组=U 币池按 venue 拆（前端 group 过滤直渲）。
	if nav, err := s.store.ComputeNav(ctx, now); err == nil {
		var idle, earning float64
		// nav 组收成 venue 级（池=venue×symbol 粒度，114 行灰尘币不可读）：
		// USD 族 equity 同 venue 内加总（D-534 同记帐单位族）；非 USD 族池不折算——
		// 每 venue 汇一行「其他币种 N 种」计数，跨币永不加总。
		usdEq := map[string]float64{}
		altN := map[string]int{}
		var vOrder []string
		for _, p := range nav.Pools {
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
		for _, p := range nav.Pools {
			if p.Ghost {
				continue
			}
			if p.Currency != "USDT" && p.Currency != "USD" {
				continue
			}
			if p.VenueType == "earn" {
				earning += p.Equity
				resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
					Group: "earning", Label: p.Venue, Ccy: p.Currency, Amount: p.Equity, Sub: p.VenueType,
				})
			} else {
				idle += p.Balance
				resp.Breakdowns = append(resp.Breakdowns, &v1.OverviewBreakdownRow{
					Group: "idle", Label: p.Venue, Ccy: p.Currency, Amount: p.Balance, Sub: p.VenueType,
				})
			}
		}
		// principal 组=每 venue 对本金的贡献（earn 池记 equity、余记 balance——
		// 与 PrincipalTotalUsd 同口径的逐项分解，和卡值对得上）。
		acc := map[string]*v1.OverviewBreakdownRow{}
		var order []string
		for _, p := range nav.Pools {
			if p.Ghost || (p.Currency != "USDT" && p.Currency != "USD") {
				continue
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
	// 同拍喂 §6：perf 组=基线→现值→外部净流→真业绩分解行；invested 组=基线锚+按
	// venue 拆的基线后净充提（投资人累计投入=基线+净充提，构成项与卡值对得上）。
	if perf, err := s.store.PerfSinceBaseline(ctx, now); err == nil && perf != nil {
		resp.PerfAvailable = true
		resp.BaselineUsd = perf.BaselineUSD
		resp.BaselineAt = timestamppb.New(perf.BaselineAt)
		resp.PerfUsd = perf.PerfUSD
		resp.PerfAnnPct = perf.PerfAnnPct
		resp.PerfFlowProvisional = perf.FlowProvisional
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

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
