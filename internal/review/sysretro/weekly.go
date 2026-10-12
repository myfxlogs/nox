// weekly.go 周报装配：§1-4 Go 算表+AI 叙事，§5 AI 建议→收件箱。
package sysretro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"arb/internal/ir/inboxfmt"
	"arb/internal/platform/analyst"
	"arb/internal/review/kbreport"
	"arb/internal/store/pgstore"
	"gopkg.in/yaml.v3"
)

// weeklySystemPrompt 周报 AI 约束：节标题照抄，数字不改，§5 后必须跟 sysretro-suggestions 块。
const weeklySystemPrompt = `你是 ARB 系统的周复盘员。用户消息里 fenced 块中的数字由 Go 确定性计算——禁止改动、重算或复述时写错。
按以下五个二级标题逐节输出（标题照抄，每节 2-4 句叙事判断，不复述表格）：
## 交易成败归因
## 纸面交易复盘
## 策略生死状
## 踩坑与经验
## 改进建议
「改进建议」≤5 条，每条一行「标题|理由|建议动作」；本节之后必须紧跟一个 ` + "```sysretro-suggestions" + ` fenced 块重复同样的行（供程序解析进收件箱，块外文字不进收件箱）。
注意：预估 vs 实收的差是口径差不是错误（pos502 先例）；原因归因而非裁决——淘汰走既有毕业链。
数据块尾含「三所深度（近7日）」观测表（spot 资金迁移观察窗输入）：叙事可引用其分布结论，照表说话禁改数。`

// GenerateWeekly 生成周报。窗口=[now-7d, now)（D-510 S4：7d 滚动窗无历日歧义不改；
// 触发点随 S1 移至周一 00:30 北京时间，表头/prompt 注明北京口径）。落档 sysretro-weekly-YYYY-MM-DD.md。
func GenerateWeekly(ctx context.Context, deps Deps) (string, error) {
	deps.fill()
	if deps.Store == nil {
		return "", fmt.Errorf("sysretro: store 未配置")
	}
	if deps.Client == nil {
		return "", fmt.Errorf("sysretro: client 未配置")
	}
	now := deps.Now().UTC()
	since := now.Add(-7 * 24 * time.Hour)
	w, err := deps.Store.RetroWeeklyStats(ctx, since, now)
	if err != nil {
		return "", fmt.Errorf("weekly stats: %w", err)
	}
	kbDelta, err := kbDelta(deps.EntriesDir, since, now)
	if err != nil {
		kbDelta = fmt.Sprintf("(kb scan err: %v)", err)
	}
	zSigma, zLim := 2.0, 2.0
	if v, ok, cerr := deps.Store.AppConfigFloat(ctx, "ARB_ELIM_ZSIGMA"); cerr == nil && ok && v > 0 {
		zSigma = v
	}
	if v, ok, cerr := deps.Store.AppConfigFloat(ctx, "ARB_RETRO_DEP_Z"); cerr == nil && ok && v > 0 {
		zLim = v
	}
	statsBlock := renderWeeklyStats(w, kbDelta, zSigma, zLim)
	// 三所深度章节（读败=注记降级不炸周报）。
	if dr, derr := deps.Store.DepthFacts(ctx, since, now); derr != nil {
		statsBlock += "== 三所深度（近7日）==\n(读取失败: " + derr.Error() + ")\n"
	} else {
		statsBlock += "== 三所深度（近7日）==\n" + depthWeeklyTable(dr)
	}

	prompt := fmt.Sprintf("以下是 %s 起 7 日窗口运行统计（Go 确定性计算）：\n\n```\n%s```\n\n请按系统提示词输出五节复盘。",
		since.Format("2006-01-02"), statsBlock)
	var aiText strings.Builder
	_, err = deps.Run(ctx, deps.Client, deps.Tools, retroToolSpecs(), weeklySystemPrompt,
		[]analyst.Message{analyst.TextContent("user", prompt)},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return "", fmt.Errorf("analyst run: %w", err)
	}

	date := now.Format("2006-01-02")
	report := assembleWeekly(date, deps.Client.Model, w, kbDelta, aiText.String(), zSigma, zLim)
	path, err := writeReport(deps.ReportDir, "sysretro-weekly-"+date+".md", report)
	if err != nil {
		return "", err
	}
	// §5 建议 → 收件箱（AI 提议≠已验证知识，分诊在席位）。
	suggs, malformed := parseSuggestions(aiText.String())
	if malformed {
		deps.alert(ctx, "warn", "[系统复盘] 周报 AI 输出格式异常（已按截断内容容忍解析，建议集可能不完整）")
	}
	if len(suggs) > 0 {
		rows := make([]inboxfmt.Row, 0, len(suggs))
		for _, sg := range suggs {
			rows = append(rows, inboxfmt.Row{Source: inboxfmt.SrcSysretroWeekly, Title: sg.title, Reason: sg.reason, Action: sg.action})
		}
		if err := inboxfmt.Append(deps.InboxPath, date, rows); err != nil {
			return path, fmt.Errorf("append inbox: %w", err)
		}
	}
	deps.alert(ctx, "info", "[系统复盘] 周报已生成："+path)
	return path, nil
}

// entryMeta KB 条目最小 frontmatter（周报 §4 窗口增量用）。
type entryMeta struct {
	ID           string `yaml:"id"`
	Status       string `yaml:"status"`
	FirstSeen    string `yaml:"first_seen"`
	LastVerified string `yaml:"last_verified"`
}

// kbDelta KB 窗口增量（新增/退役/falsified 计数+id 清单）。
func kbDelta(entriesDir string, since, now time.Time) (string, error) {
	matches, err := filepath.Glob(filepath.Join(entriesDir, "*.md"))
	if err != nil {
		return "", err
	}
	var added, retired, falsified []string
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var e entryMeta
		content := string(data)
		i := strings.Index(content, "---\n")
		if i < 0 {
			continue
		}
		j := strings.Index(content[i+4:], "\n---")
		if j < 0 {
			continue
		}
		if yaml.Unmarshal([]byte(content[i+4:i+4+j]), &e) != nil {
			continue
		}
		if d, perr := time.Parse("2006-01-02", strings.TrimSpace(e.FirstSeen)); perr == nil &&
			!d.Before(since) && !d.After(now) {
			added = append(added, e.ID)
		}
		switch e.Status {
		case "retired":
			retired = append(retired, e.ID)
		case "falsified":
			falsified = append(falsified, e.ID)
		}
	}
	// kbreport.Scan 快照行（总量背书）。
	stats, _ := kbreport.Scan(entriesDir, now)
	sort.Strings(added)
	sort.Strings(retired)
	sort.Strings(falsified)
	var b strings.Builder
	fmt.Fprintf(&b, "kb_total: %d\nkb_added_7d: %d %s\nkb_retired: %d %s\nkb_falsified: %d %s\n",
		stats.Total, len(added), strings.Join(added, " "),
		len(retired), strings.Join(retired, " "), len(falsified), strings.Join(falsified, " "))
	return b.String(), nil
}

// renderWeeklyStats §1-4 统计快照块。
func renderWeeklyStats(w *pgstore.RetroWeekly, kb string, zSigma, zLim float64) string {
	var b strings.Builder
	b.WriteString("== 平仓分组（type）==\n")
	for _, g := range w.Groups {
		fmt.Fprintf(&b, "%s: n=%d win=%d loss=%d pnl=$%.4f est_bps=%.2f real_bps=%.2f\n",
			g.Type, g.N, g.Wins, g.Losses, g.PnlUSD, g.EstBps, g.RealBps)
	}
	if len(w.Groups) == 0 {
		b.WriteString("(none)\n")
	}
	b.WriteString("== 典型案例 ==\n")
	caseLine := func(tag string, c *pgstore.RetroClosedCase) {
		if c == nil {
			fmt.Fprintf(&b, "%s: (none)\n", tag)
			return
		}
		fmt.Fprintf(&b, "%s: pos%d %s pnl=$%.4f est_bps=%.2f real_bps=%.2f\n",
			tag, c.ID, c.Type, c.PnlUSD, c.EstBps, c.RealBps)
	}
	caseLine("top_win", w.TopWin)
	caseLine("top_loss", w.TopLoss)
	caseLine("top_deviation", w.TopDeviation)
	b.WriteString("== 纸面交易 ==\n")
	fmt.Fprintf(&b, "paper_spot_carry exits_7d=%d reasons: %s\n", w.PaperSpotN, joinDist(w.PaperSpot))
	b.WriteString("== 策略生死 ==\n")
	for _, s := range w.Strategies {
		dis := ""
		if s.Disabled {
			dis = " DISABLED(" + s.Reason + ")"
		}
		fmt.Fprintf(&b, "%s: trades=%d win_rate=%.2f pnl=$%.4f %s%s\n%s", s.Strategy, s.Trades, s.WinRate, s.TotalPnl, hprText(s), dis, strategyEvidence(s, w.DepSeq[s.Strategy], zSigma, zLim))
	}
	b.WriteString("== 告警聚类（warn/critical top10）==\n")
	for _, c := range w.AlertCluster {
		fmt.Fprintf(&b, "%s [%s] x%d\n", c.Key, c.Level, c.Count)
	}
	fmt.Fprintf(&b, "evals_7d: %s\n", joinDist(w.EvalDist))
	b.WriteString("== KB 增量 ==\n" + kb)
	return b.String()
}

// assembleWeekly 五节契约拼装：Go 表 fenced + AI 叙事（按节标题抽取，缺节回退尾部整段）。
func assembleWeekly(date, model string, w *pgstore.RetroWeekly, kb, aiText string, zSigma, zLim float64) string {
	aiSections := splitAISections(aiText)
	var b strings.Builder
	fmt.Fprintf(&b, "# 系统周复盘 %s\n\n> 数字=Go 确定性计算；叙事=AI 生成（%s）未经独立复审；§改进建议已入收件箱待分诊。\n\n", date, model)
	writeSection := func(title, table string) {
		fmt.Fprintf(&b, "## %s\n\n```\n%s\n```\n\n%s\n\n", title, strings.TrimSpace(table), strings.TrimSpace(aiSections[title]))
	}
	writeSection("交易成败归因", weeklyTradeTable(w))
	writeSection("纸面交易复盘", weeklyPaperTable(w))
	writeSection("策略生死状", weeklyStrategyTable(w, zSigma, zLim))
	writeSection("踩坑与经验", kb+alertClusterTable(w))
	// §5：AI 全文中的建议节（含 fenced 块原文）。
	sugg := strings.TrimSpace(aiSections["改进建议"])
	if sugg == "" {
		sugg = "(AI 未产出建议节)"
	}
	fmt.Fprintf(&b, "## 改进建议\n\n%s\n", sugg)
	return b.String()
}

// splitAISections 按 `## ` 标题抽取 AI 输出各节正文。
func splitAISections(text string) map[string]string {
	out := map[string]string{}
	var cur string
	var buf strings.Builder
	flush := func() {
		if cur != "" {
			out[cur] = strings.TrimSpace(buf.String())
		}
	}
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "## ") {
			flush()
			cur = strings.TrimSpace(strings.TrimPrefix(ln, "## "))
			buf.Reset()
			continue
		}
		if cur != "" {
			buf.WriteString(ln + "\n")
		}
	}
	flush()
	return out
}

func weeklyTradeTable(w *pgstore.RetroWeekly) string {
	var b strings.Builder
	for _, g := range w.Groups {
		fmt.Fprintf(&b, "%s n=%d win=%d loss=%d pnl=$%.4f est_bps=%.2f real_bps=%.2f\n",
			g.Type, g.N, g.Wins, g.Losses, g.PnlUSD, g.EstBps, g.RealBps)
	}
	if len(w.Groups) == 0 {
		b.WriteString("(none)")
	}
	caseLine := func(tag string, c *pgstore.RetroClosedCase) {
		if c == nil {
			fmt.Fprintf(&b, "\n%s: (none)", tag)
			return
		}
		fmt.Fprintf(&b, "\n%s: pos%d %s pnl=$%.4f est=%.2fbps real=%.2fbps", tag, c.ID, c.Type, c.PnlUSD, c.EstBps, c.RealBps)
	}
	caseLine("top_win", w.TopWin)
	caseLine("top_loss", w.TopLoss)
	caseLine("top_deviation", w.TopDeviation)
	return b.String()
}

func weeklyPaperTable(w *pgstore.RetroWeekly) string {
	return fmt.Sprintf("paper_spot_carry exits_7d=%d reasons: %s",
		w.PaperSpotN, joinDist(w.PaperSpot))
}

// hprText D-181 资本效率列：per-trade avg(net/notional) 转 bps；口径缺如实标。
func hprText(s pgstore.RetroStrategyRow) string {
	if s.HprAvg == nil || s.HprN == 0 {
		return "hpr_bps=口径缺"
	}
	return fmt.Sprintf("hpr_bps=%.2f(%d/%d)", *s.HprAvg*1e4, s.HprN, s.HprAll)
}

func alertClusterTable(w *pgstore.RetroWeekly) string {
	var b strings.Builder
	for _, c := range w.AlertCluster {
		fmt.Fprintf(&b, "%s [%s] x%d\n", c.Key, c.Level, c.Count)
	}
	fmt.Fprintf(&b, "evals_7d: %s\n", joinDist(w.EvalDist))
	return b.String()
}
