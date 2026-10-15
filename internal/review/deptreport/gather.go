// gather.go —— 公司日报输入段装配（D-246 v3 §4.4 money-first 固定序，全确定性：
// 数字只来自 PG/Go 计算结果渲染文本，LLM 侧禁编数纪律见 run 系统提示）。
// §4.3 六项确定性必备指标置顶；七段固定序：
// ①NAV/闲置比 → ②在仓+carry 实收 → ③待决提案含 age → ④请求箱投资人侧/系统侧
// 分列 → ⑤策略态变 → ⑥事故硬线 → ⑦流健康。
package deptreport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// gather 装配公司日报输入（任一查询面失败即返错=当班跳过，零半成品报告）。
func gather(ctx context.Context, d *Deps, dayStart, dayEnd time.Time) (string, error) {
	rd, err := d.Store.RetroDailyStats(ctx, dayStart, dayEnd)
	if err != nil {
		return "", fmt.Errorf("retro daily: %w", err)
	}
	pools, err := d.Store.ExplorerIdleFunds(ctx, dayEnd)
	if err != nil {
		return "", fmt.Errorf("idle funds: %w", err)
	}
	net30, err := d.Store.NetRealizedSince(ctx, dayEnd.Add(-30*24*time.Hour), false)
	if err != nil {
		return "", fmt.Errorf("net30: %w", err)
	}
	netMonth, err := d.Store.NetRealizedSince(ctx, monthStart(dayEnd), false)
	if err != nil {
		return "", fmt.Errorf("net month: %w", err)
	}
	llmCost, llmCalls, err := d.Store.LLMCallCostSince(ctx, monthStart(dayEnd))
	if err != nil {
		return "", fmt.Errorf("llm cost: %w", err)
	}
	props, err := d.Store.ListProposals(ctx, "proposed", 50)
	if err != nil {
		return "", fmt.Errorf("proposals: %w", err)
	}
	gaps, err := d.Store.ListConditionGaps(ctx, "")
	if err != nil {
		return "", fmt.Errorf("gaps: %w", err)
	}
	disabled, err := d.Store.ListDisabledStrategies(ctx)
	if err != nil {
		return "", fmt.Errorf("strategies: %w", err)
	}

	// D-264 M4.4②：NAV/闲置 读 pgstore.ComputeNav 单函数（IR/配置器同径，口径单源；
	// USD-only 过滤内聚——非 USD 池跳过不计不假换算）。
	navRep, err := d.Store.ComputeNav(ctx, dayEnd)
	if err != nil {
		return "", fmt.Errorf("nav: %w", err)
	}
	nav, idle := navRep.NavUSD, navRep.IdleUSD // USD 桶（-3% 硬线分母口径不动）
	var b strings.Builder
	fmt.Fprintf(&b, "报告日期：%s（北京时间历日）\n\n", dayStart.In(BJTime).Format("2006-01-02"))

	// §4.3 六项确定性必备指标（Go 算定原值，LLM 只能引用）。
	b.WriteString("【必备指标】\n")
	hardLine := ""
	if d.CapitalBaseUSD > 0 {
		hardLine = fmt.Sprintf("（对 −3%% 硬线=%.0f USD：%s）",
			-d.CapitalBaseUSD*0.03, hardLineStatus(net30, -d.CapitalBaseUSD*0.03))
	}
	idlePct := 0.0
	if nav > 0 {
		idlePct = idle / nav * 100
	}
	// D-522④：日报 NAV 按结算币种分行展示（跨币永不加总）。
	ccys := make([]string, 0, len(navRep.ByCcy))
	for c := range navRep.ByCcy {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	for _, c := range ccys {
		bk := navRep.ByCcy[c]
		pct := 0.0
		if bk.Nav > 0 {
			pct = bk.Idle / bk.Nav * 100
		}
		fmt.Fprintf(&b, "- NAV=%.2f %s｜闲置=%.2f %s（%.1f%%）\n", bk.Nav, c, bk.Idle, c, pct)
	}
	if len(ccys) == 0 {
		fmt.Fprintf(&b, "- NAV=%.2f USD｜闲置=%.2f USD（%.1f%%）\n", nav, idle, idlePct)
	}
	fmt.Fprintf(&b, "- 30d 净额=%.2f USD%s\n", net30, hardLine)
	fmt.Fprintf(&b, "- 提案中位龄=%s\n", ageFmt(medianAge(props, dayEnd)))
	fmt.Fprintf(&b, "- 最老投资人侧请求龄=%s\n", ageFmt(oldestGapAge(gaps, dayEnd)))
	fmt.Fprintf(&b, "- funding 当日实收=%.2f｜预估=%.2f USD（估实偏离=%s）\n",
		rd.FundingActualUSD, rd.FundingEstUSD, devLine(rd.FundingEstUSD, rd.FundingActualUSD))
	fmt.Fprintf(&b, "- 本月 LLM 成本=%.2f USD（%d 次调用）｜本月净额=%.2f USD\n\n",
		llmCost, llmCalls, netMonth)

	// ① NAV 与闲置
	b.WriteString("【① NAV 与闲置资金】\n")
	if len(pools) == 0 {
		b.WriteString("- （无快照行）\n")
	}
	for _, p := range pools {
		ghost := ""
		if p.Ghost {
			ghost = "｜幽灵池（账户移除——不计 NAV）"
		}
		fmt.Fprintf(&b, "- %s/%s：equity=%.2f balance=%.2f free_margin=%.2f %s｜income30d=%.2f%s\n",
			p.Venue, p.VenueType, p.Equity, p.Balance, p.FreeMargin, p.Currency, p.Income30dUSD, ghost)
	}
	if navRep.SkippedGhost > 0 {
		fmt.Fprintf(&b, "- 幽灵池剔除=%d（broker_accounts 已移除户快照残行）\n", navRep.SkippedGhost)
	}
	// ② 在仓+carry 实收
	b.WriteString("\n【② 在仓与 carry 实收】\n")
	if len(rd.OpenPos) == 0 {
		b.WriteString("- （无 OPEN 持仓）\n")
	}
	for _, p := range rd.OpenPos {
		fmt.Fprintf(&b, "- #%d %s @%s｜carry实收=%.2f USD｜开仓=%s\n",
			p.ID, p.Type, p.Venue, p.CarryUSD, p.OpenedAt.Format("01-02 15:04"))
	}
	fmt.Fprintf(&b, "- 当日开仓=%d｜当日平仓=%d｜毕业建议=%d\n", rd.OpenedToday, rd.ClosedToday, rd.GraduationsToday)
	// ③ 待决提案
	b.WriteString("\n【③ 待决提案（含龄）】\n")
	if len(props) == 0 {
		b.WriteString("- （无）\n")
	}
	for _, p := range props {
		apr := "报价缺"
		if p.ExpectedAPR != nil {
			apr = fmt.Sprintf("apr=%.4g", *p.ExpectedAPR)
		}
		fmt.Fprintf(&b, "- #%d %s @%s（轨=%s，%s）｜龄=%s\n",
			p.ID, p.TemplateID, p.Venue, p.ExecTrack, apr, ageFmt(dayEnd.Sub(p.CreatedAt)))
	}
	// ④ 请求箱（投资人侧/系统侧分列）
	b.WriteString("\n【④ 请求箱】\n")
	b.WriteString("- 投资人侧（requested，需业主动作）：\n")
	investor := 0
	for _, g := range gaps {
		if g.Status != "requested" {
			continue
		}
		investor++
		fmt.Fprintf(&b, "  · #%d %s %s:%s —— %s\n", g.ID, g.TemplateID, g.Kind, g.Subject, g.RequestText)
	}
	if investor == 0 {
		b.WriteString("  · （无）\n")
	}
	b.WriteString("- 系统侧（dormant/休眠轨，零业主动作）：\n")
	system := 0
	for _, g := range gaps {
		if g.Status != "waived" {
			continue
		}
		system++
		fmt.Fprintf(&b, "  · #%d %s %s:%s（waived）\n", g.ID, g.TemplateID, g.Kind, g.Subject)
	}
	if system == 0 {
		b.WriteString("  · （无）\n")
	}
	// ⑤ 策略态变
	b.WriteString("\n【⑤ 策略态变】\n")
	if len(disabled) == 0 {
		b.WriteString("- （无禁用策略）\n")
	} else {
		ks := make([]string, 0, len(disabled))
		for k := range disabled {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Fprintf(&b, "- 禁用集：%s\n", strings.Join(ks, " "))
	}
	// ⑥ 事故与硬线
	b.WriteString("\n【⑥ 事故与硬线】\n")
	b.WriteString("- 告警分布：" + distLine(rd.AlertDist) + "\n")
	for i, m := range rd.AlertTop3 {
		if i >= 3 {
			break
		}
		fmt.Fprintf(&b, "- 告警Top%d：%s\n", i+1, m)
	}
	if hardLine != "" {
		fmt.Fprintf(&b, "- 30d 硬线状态：%s\n", hardLineStatus(net30, -d.CapitalBaseUSD*0.03))
	}
	// ⑦ 流健康
	b.WriteString("\n【⑦ 流健康】\n")
	fmt.Fprintf(&b, "- 流事件（断/重连/zero-frame）=%d｜评估分布=%s\n", rd.StreamEvents, distLine(rd.EvalDist))

	return b.String(), nil
}

// monthStart 当月 1 日零时（t 所在时区历月）。
func monthStart(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
}

// medianAge proposed 提案创建龄中位数（零行=0）。
func medianAge(props []pgstore.Proposal, now time.Time) time.Duration {
	if len(props) == 0 {
		return 0
	}
	ages := make([]time.Duration, 0, len(props))
	for _, p := range props {
		ages = append(ages, now.Sub(p.CreatedAt))
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i] < ages[j] })
	return ages[len(ages)/2]
}

// oldestGapAge 最老投资人侧请求龄（requested 行 created_at 原值；无=0）。
func oldestGapAge(gaps []pgstore.ConditionGap, now time.Time) time.Duration {
	var oldest time.Duration
	for _, g := range gaps {
		if g.Status != "requested" {
			continue
		}
		if a := now.Sub(g.CreatedAt); a > oldest {
			oldest = a
		}
	}
	return oldest
}

// ageFmt 时长人读化（>48h 按天）。
func ageFmt(d time.Duration) string {
	if d <= 0 {
		return "（无）"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	}
	return fmt.Sprintf("%.1f 小时", d.Hours())
}

// hardLineStatus 30d 净额对硬线状态词。
func hardLineStatus(net, line float64) string {
	if net <= line {
		return "触碰（停新开）"
	}
	return "未触碰"
}

// devLine 预估 vs 实收偏离描述。
func devLine(est, act float64) string {
	if est == 0 {
		if act == 0 {
			return "零偏离"
		}
		return "预估缺"
	}
	return fmt.Sprintf("%.1f%%", (act-est)/est*100)
}

// distLine map 分布渲染（键序稳定——输出确定性，报告 diff 可审）。
func distLine(m map[string]int) string {
	if len(m) == 0 {
		return "（无）"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
