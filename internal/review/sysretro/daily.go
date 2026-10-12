// daily.go 日报装配：§1-3+§5 全 Go 机械产出，§4 AI 小结（3-5 句）。
package sysretro

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"arb/internal/platform/analyst"
	"arb/internal/store/pgstore"
)

// dailySystemPrompt 日报 AI 约束：数字已在 prompt，只写判断不长篇。
const dailySystemPrompt = `你是 ARB 系统的日结员。用户消息里 fenced 块中的数字由 Go 确定性计算——禁止改动、重算或复述时写错。
只输出一段「今日小结」正文（3-5 句，无标题无列表）：值得记的事、异常、明日关注点。不输出其他任何内容。`

// GenerateDaily 生成日报。窗口=昨日北京时间全天 [BJ 00:00, BJ 24:00)（D-510：00:30 BJ 触发
// 时昨日数据已齐；BJ 00:00=UTC 16:00=binance 8h funding 结算边界，accrual 归因零漂移）。
// 返回落档路径 docs/报告/sysretro-daily-YYYY-MM-DD.md（文件名=昨日北京日期，同日幂等覆盖）。
func GenerateDaily(ctx context.Context, deps Deps) (string, error) {
	deps.fill()
	if deps.Store == nil {
		return "", fmt.Errorf("sysretro: store 未配置")
	}
	if deps.Client == nil {
		return "", fmt.Errorf("sysretro: client 未配置")
	}
	nowBJ := deps.Now().In(BJTime)
	dayEnd := time.Date(nowBJ.Year(), nowBJ.Month(), nowBJ.Day(), 0, 0, 0, 0, BJTime)
	dayStart := dayEnd.AddDate(0, 0, -1)
	day := dayStart.Format("2006-01-02")
	st, err := deps.Store.RetroDailyStats(ctx, dayStart, dayEnd)
	if err != nil {
		return "", fmt.Errorf("daily stats: %w", err)
	}
	statsBlock := renderDailyStats(st, deps.Now())
	// 三所深度当日快照行（读败=注记降级不炸日报）。
	if dr, derr := deps.Store.DepthFacts(ctx, dayStart, dayEnd); derr != nil {
		statsBlock += "三所深度当日: (读取失败)\n"
	} else {
		statsBlock += depthDailyLine(dr) + "\n"
	}

	prompt := fmt.Sprintf("以下是 %s 昨日（北京时间全天）运行统计（Go 确定性计算）：\n\n```\n%s```\n\n请输出今日小结。",
		day, statsBlock)
	var aiText strings.Builder
	_, err = deps.Run(ctx, deps.Client, deps.Tools, retroToolSpecs(), dailySystemPrompt,
		[]analyst.Message{analyst.TextContent("user", prompt)},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return "", fmt.Errorf("analyst run: %w", err)
	}

	report := assembleDaily(deps.Now(), day, st, statsBlock, strings.TrimSpace(aiText.String()), deps)
	path, err := writeReport(deps.ReportDir, "sysretro-daily-"+day+".md", report)
	if err != nil {
		return "", err
	}
	deps.alert(ctx, "info", "[系统复盘] 日报已生成："+path)
	return path, nil
}

// renderDailyStats §1-3 统计快照块（嵌 prompt + 报告附录的数字唯一源）。
func renderDailyStats(d *pgstore.RetroDaily, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "open_positions: %d\n", len(d.OpenPos))
	for _, p := range d.OpenPos {
		fmt.Fprintf(&b, "  pos%d %s venue=%s age=%s carry=$%.4f\n",
			p.ID, p.Type, p.Venue, now.Sub(p.OpenedAt).Round(time.Hour), p.CarryUSD)
	}
	fmt.Fprintf(&b, "opened_today: %d  closed_today: %d\n", d.OpenedToday, d.ClosedToday)
	fmt.Fprintf(&b, "graduations_today: %d\n", d.GraduationsToday) // D-468 S4：毕业建议 新增=N
	fmt.Fprintf(&b, "funding_actual_today: $%.4f  funding_estimated_today: $%.4f\n",
		d.FundingActualUSD, d.FundingEstUSD)
	b.WriteString("evals_today: " + joinDist(d.EvalDist) + "\n")
	b.WriteString("alerts_today: " + joinDist(d.AlertDist) + "\n")
	for i, m := range d.AlertTop3 {
		fmt.Fprintf(&b, "alert_top%d: %s\n", i+1, m)
	}
	fmt.Fprintf(&b, "stream_events_today: %d\n", d.StreamEvents)
	return b.String()
}

// assembleDaily 五节契约拼装（§1-3 Go 表 → §4 AI → §5 交接面 Go）。day=昨日北京历日日期。
func assembleDaily(now time.Time, day string, d *pgstore.RetroDaily, stats, aiText string, deps Deps) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 系统日结 %s\n\n> 数字=Go 确定性计算（昨日北京时间全天 [00:00,24:00)）；§今日小结=AI 生成未经独立复审。\n\n", day)
	b.WriteString("## 今日盘面\n\n```\n" + stats + "```\n\n")
	b.WriteString("## 今日决策轨\n\n" + "```\n" + joinDist(d.EvalDist) + "\n```\n\n")
	b.WriteString("## 今日事件\n\n```\n" + joinDist(d.AlertDist) + "\n")
	for i, m := range d.AlertTop3 {
		fmt.Fprintf(&b, "top%d: %s\n", i+1, m)
	}
	fmt.Fprintf(&b, "stream_events: %d\n```\n\n", d.StreamEvents)
	b.WriteString("## 今日小结\n\n" + aiText + "\n\n")
	b.WriteString("## 交接面\n\n" + handoffBlock(deps, now) + "\n")
	return b.String()
}

// joinDist map→确定性 "k=n k=n"（key 排序）。
func joinDist(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}
