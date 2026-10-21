// service_overview_bundle.go —— D-536 总览页聚合（一次返回五段；前端零运算只渲染）。
package dashboard

import (
	"context"
	"fmt"
	"regexp"
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
	pf := s.prefetchOverview(ctx, now, resp)
	navPools, orders, perf, poss, papers, alerts := pf.navPools, pf.orders, pf.perf, pf.poss, pf.papers, pf.alerts
	n30, n30ok, killDone := pf.n30, pf.n30ok, pf.killDone
	// 锁定档=在仓单披露子行（不进本金总计——D-543R1 实证 17,199 双计：fin_dci_order
	// 的 filled 行含 earn 申购单，其本金已在 venue earn 快照桶内，再叠加必双计）。
	locked := 0.0
	var earliest *time.Time
	// 活期聚合桶只留订单行派生元数据（笔数/首笔申购日）——金额源=快照 earn 桶
	// （fin_dci_order filled 行=申购事件日志非持仓，赎回/轮动不翻态→Σ 虚高）。
	// 键=venue+" 活期 "+InvestCcy（语义币种字段——USDT001 型产品编号归位 USDT
	// 桶，与快照桶 currency 键对拍不错配）。
	earnMeta := map[string]struct {
		n     int
		first time.Time
	}{}
	if len(orders) > 0 {
		for _, o := range orders {
			if o.Status != "filled" {
				continue
			}
			// 订单卡中文化派生（显示层——存储零动）：**语义字段分型**（opt_type/template_id
			// ——USDT001 实为 binance 活期申购被错标双币的业主实拍修正：禁 product_ref
			// 字符串嗅探，类型判定认语义字段=协议面取证 skill ④镜像教训）+label 友好化。
			kind := "dci"
			label := o.ProductRef
			switch {
			case o.OptType == "EARN" || o.TemplateID == "cash_mgmt" || o.TemplateID == "earn_apr_lending" || o.TemplateID == "earn_rotation":
				kind = "earn"
				label = o.Venue + " 活期 " + o.InvestCcy // 键=币种域（与快照桶同尺）
			}
			if kind == "earn" {
				if o.InvestCcy != "USDT" && o.InvestCcy != "USD" {
					continue // 卡域限 USDT/USD（alt 池原生单位不冒 USD——退回①伤①）
				}
				g := earnMeta[label]
				g.n++
				if g.first.IsZero() || o.CreatedAt.Before(g.first) {
					g.first = o.CreatedAt
				}
				earnMeta[label] = g
				continue // earn 活期=流动非锁定——不进 locked 档
			}
			locked += o.Qty
			if o.TenorDays != nil {
				t := o.CreatedAt.Add(time.Duration(*o.TenorDays) * 24 * time.Hour)
				if earliest == nil || t.Before(*earliest) {
					earliest = &t
				}
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
	// earn 聚合卡（金额降序）：金额=快照 earn 桶 equity（venue_type='earn'×币种
	// label——与 PrincipalEarningUsd 同源真值）；快照桶缺=如实标缺禁回落订单Σ。
	snapAmt := map[string]float64{}
	for _, p := range navPools {
		// 卡域限 USDT/USD（与 PrincipalEarningUsd 同域滤——alt 池原生单位
		// 数量直塞 AmountUsd=单位错显，退回①伤①：PEPE 46155 霸榜首实证）。
		if p.VenueType == "earn" && !p.Ghost && (p.Currency == "USDT" || p.Currency == "USD") {
			snapAmt[p.Venue+" 活期 "+p.Currency] += p.Equity
		}
	}
	var earnRows []*v1.OverviewOrderRow
	for label, amt := range snapAmt {
		m := earnMeta[label]
		ttl := label
		if m.n > 0 {
			ttl = fmt.Sprintf("%s（%d 笔）", label, m.n)
		}
		row := &v1.OverviewOrderRow{Kind: "earn", Label: ttl, AmountUsd: amt, Status: "LIVE"}
		if !m.first.IsZero() {
			row.Until = timestamppb.New(m.first)
		}
		earnRows = append(earnRows, row)
		delete(earnMeta, label)
	}
	for label, m := range earnMeta { // 订单行在而快照桶缺=断供/已清——如实标缺
		row := &v1.OverviewOrderRow{Kind: "earn", Label: fmt.Sprintf("%s（%d 笔）", label, m.n), Status: "缺快照"}
		if !m.first.IsZero() {
			row.Until = timestamppb.New(m.first)
		}
		earnRows = append(earnRows, row)
	}
	sort.Slice(earnRows, func(i, j int) bool { return earnRows[i].AmountUsd > earnRows[j].AmountUsd })
	resp.Orders = append(resp.Orders, earnRows...)
	resp.PrincipalLockedUsd = locked // 披露值：在息面中锁定子档，非第三桶钱
	if earliest != nil {
		resp.EarliestRelease = timestamppb.New(*earliest)
	}
	resp.PrincipalTotalUsd = resp.PrincipalIdleUsd + resp.PrincipalEarningUsd // 快照池即账本真值
	// §5 十段工序健康读模（S3′-b：后端聚合 emit 时戳+wait_reason+错误面——
	// 禁前端拿 dept 拼状态/禁前端判故障）。
	// §2 业绩（D-535 锚面——缺锚/读败 available=false 如实缺；报告留手喂产出面）。
	if perf != nil {
		resp.PerfAvailable = true
		resp.BaselineUsd = perf.BaselineUSD
		resp.BaselineAt = timestamppb.New(perf.BaselineAt)
		resp.PerfUsd = perf.PerfUSD
		resp.PerfAnnPct = perf.PerfAnnPct
		resp.PerfFlowProvisional = perf.FlowProvisional
	}
	// §3 autotrade 在仓行+纸面计数（标沙箱）。
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
	resp.PaperCount = int32(len(papers))
	// §6-§10 产出面（棘轮拆 service_overview_output.go——组成明细/归因/漏斗/等待面/眠钱）。
	s.appendOverviewOutput(ctx, resp, navPools, perf)
	// §4 风险心跳（kill 态+30d 净额对硬线+24h critical 数）。
	if !killDone {
		resp.KillSwitch = true // D-620ⓑ：展示 fail-closed——宁显停不显通
	}
	if n30ok {
		resp.Net30Usd = n30
	}
	if len(alerts) > 0 {
		cut := now.Add(-24 * time.Hour)
		var crits []struct {
			msg string
			ts  time.Time
		}
		for _, a := range alerts {
			if a.Level == "critical" && a.Ts.After(cut) {
				resp.CritAlerts_24H++
				// 业主令 10-17：明细随段（可见「是什么红」非只计数）——文案原文直透不裁剪。
				crits = append(crits, struct {
					msg string
					ts  time.Time
				}{a.Message, a.Ts})
			}
		}
		// 归组+封顶（业主报障 10-18：30 条同源告警霸屏压底）——按指纹归组
		// （变数字段剥位，critFingerprint），组内 count/first/last，卡片=最新原文；
		// ≤critCardCap 组，溢出尾行「+N 组见告警页」count=被截告警合计（Σcount 守恒）。
		type grp struct {
			latest, first time.Time
			msg           string
			n             int32
		}
		groups := map[string]*grp{}
		var order []string
		for _, c := range crits {
			fp := critFingerprint(c.msg)
			g := groups[fp]
			if g == nil {
				g = &grp{latest: c.ts, first: c.ts, msg: c.msg}
				groups[fp] = g
				order = append(order, fp)
			}
			g.n++
			if c.ts.After(g.latest) {
				g.latest, g.msg = c.ts, c.msg
			}
			if c.ts.Before(g.first) {
				g.first = c.ts
			}
		}
		hiddenAlerts := int32(0)
		for i, fp := range order {
			g := groups[fp]
			if i >= critCardCap {
				hiddenAlerts += g.n
				continue
			}
			resp.CritAlerts = append(resp.CritAlerts, &v1.OverviewCritAlert{
				Message: g.msg, Ts: timestamppb.New(g.latest),
				Count: g.n, FirstTs: timestamppb.New(g.first),
			})
		}
		if hidden := len(order) - minInt(len(order), critCardCap); hidden > 0 {
			resp.CritAlerts = append(resp.CritAlerts, &v1.OverviewCritAlert{
				Message: fmt.Sprintf("+%d 组见告警页", hidden), Count: hiddenAlerts,
			})
		}
	}
	return connect.NewResponse(resp), nil
}

// critCardCap 明细卡阵封顶组数（同等待面限行先例族——防同源刷屏压底）。
const critCardCap = 8

// critFingerprintDigits 归组指纹剥位规则（票面钉「stripping 规则落代码常量+测试锚」）：
// 数字打头连同后续小写字母/数字整段归一——`age=1h31m20s`→`age=#`、
// `BTCUSDT_261225`→`BTCUSDT_#`、版本号/id/时量全坍缩；文案骨架相同即同组。
var critFingerprintDigits = regexp.MustCompile(`[0-9]+[a-z0-9]*`)

func critFingerprint(msg string) string {
	return critFingerprintDigits.ReplaceAllString(msg, "#")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
