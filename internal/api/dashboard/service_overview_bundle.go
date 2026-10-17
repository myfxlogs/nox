// service_overview_bundle.go —— D-536 总览页聚合（一次返回五段；前端零运算只渲染）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	// 池行留手喂 §6-§10 产出面（appendOverviewOutput——棘轮拆出独立文件）。
	var navPools []pgstore.ExplorerIdlePool
	if nav, err := s.store.ComputeNav(ctx, now); err == nil {
		navPools = nav.Pools
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
	earnAgg := map[string]struct {
		amt   float64
		n     int
		first time.Time
		venue string
	}{} // 活期聚合桶（label→持仓位）
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
			// 订单卡中文化派生（显示层——存储零动）：**语义字段分型**（opt_type/template_id
			// ——USDT001 实为 binance 活期申购被错标双币的业主实拍修正：禁 product_ref
			// 字符串嗅探，类型判定认语义字段=协议面取证 skill ④镜像教训）+label 友好化。
			kind := "dci"
			label := o.ProductRef
			switch {
			case o.OptType == "EARN" || o.TemplateID == "cash_mgmt" || o.TemplateID == "earn_apr_lending" || o.TemplateID == "earn_rotation":
				kind = "earn"
				label = earnLabel(o.Venue, o.ProductRef)
			}
			// 业主第一性令 10-17：活期类同（所×产品）合并一卡=持仓位非流水——
			// 金额合计+笔数+首笔日；失败/已结单不构成在仓持仓不入聚（记录级流水归 DCI 面板）。
			if kind == "earn" && (o.Status == "filled" || o.Status == "LIVE" || o.Status == "live") {
				g := earnAgg[label]
				g.amt += o.Qty
				g.n++
				if g.first.IsZero() || o.CreatedAt.Before(g.first) {
					g.first = o.CreatedAt
				}
				earnAgg[label] = g
				continue
			}
			resp.Orders = append(resp.Orders, &v1.OverviewOrderRow{
				Kind:      kind,
				Label:     fmt.Sprintf("%s #%d", label, o.ID),
				AmountUsd: o.Qty,
				Status:    o.Status,
				Until:     timestamppb.New(o.CreatedAt.Add(time.Duration(derefInt(o.TenorDays)) * 24 * time.Hour)),
			})
		}
	}
	// 活期聚合卡发射（金额降序）：status=LIVE（持有中）、until=首笔申购日。
	for _, g := range sortedEarnAgg(earnAgg) {
		resp.Orders = append(resp.Orders, &v1.OverviewOrderRow{
			Kind:      "earn",
			Label:     fmt.Sprintf("%s（%d 笔）", g.label, g.n),
			AmountUsd: g.amt,
			Status:    "LIVE",
			Until:     timestamppb.New(g.first),
		})
	}
	resp.PrincipalLockedUsd = locked // 披露值：在息面中锁定子档，非第三桶钱
	if earliest != nil {
		resp.EarliestRelease = timestamppb.New(*earliest)
	}
	resp.PrincipalTotalUsd = resp.PrincipalIdleUsd + resp.PrincipalEarningUsd // 快照池即账本真值
	// §5 十段工序健康读模（S3′-b：后端聚合 emit 时戳+wait_reason+错误面——
	// 禁前端拿 dept 拼状态/禁前端判故障）。
	resp.Stages = s.pipelineStages(ctx)
	// §2 业绩（D-535 锚面——缺锚/读败 available=false 如实缺；报告留手喂产出面）。
	var perf *pgstore.PerfReport
	if p, err := s.store.PerfSinceBaseline(ctx, now); err == nil && p != nil {
		perf = p
		resp.PerfAvailable = true
		resp.BaselineUsd = p.BaselineUSD
		resp.BaselineAt = timestamppb.New(p.BaselineAt)
		resp.PerfUsd = p.PerfUSD
		resp.PerfAnnPct = p.PerfAnnPct
		resp.PerfFlowProvisional = p.FlowProvisional
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
	// §6-§10 产出面（棘轮拆 service_overview_output.go——组成明细/归因/漏斗/等待面/眠钱）。
	s.appendOverviewOutput(ctx, resp, navPools, perf, now)
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
				// 业主令 10-17：明细随段（可见「是什么红」非只计数）——文案原文直透不裁剪。
				resp.CritAlerts = append(resp.CritAlerts, &v1.OverviewCritAlert{
					Message: a.Message,
					Ts:      timestamppb.New(a.Ts),
				})
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

// sortedEarnAgg 聚合桶→金额降序列（稳定发射序）。
func sortedEarnAgg(m map[string]struct {
	amt   float64
	n     int
	first time.Time
	venue string
}) []struct {
	label string
	amt   float64
	n     int
	first time.Time
} {
	out := make([]struct {
		label string
		amt   float64
		n     int
		first time.Time
	}, 0, len(m))
	for k, v := range m {
		out = append(out, struct {
			label string
			amt   float64
			n     int
			first time.Time
		}{k, v.amt, v.n, v.first})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].amt > out[j].amt })
	return out
}

// earnLabel 理财单 label 友好化（显示层）：剥 venue 前缀的产品名（okx-saving-USDT→
// USDT、gate-uni-USDT→USDT、USDT001→USDT001 原样——binance 产品编号无前缀）。
func earnLabel(venue, ref string) string {
	if c := strings.TrimPrefix(ref, venue+"-saving-"); c != ref {
		return venue + " 活期 " + c
	}
	if c := strings.TrimPrefix(ref, venue+"-uni-"); c != ref {
		return venue + " 活期 " + c
	}
	return venue + " 活期 " + ref
}
