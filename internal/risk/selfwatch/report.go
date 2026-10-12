// report.go —— D-033①②：每日 08:30 BJ paper 窗日报 + Stage 闸就绪表。
// 口径=scripts/paper_window_report.sh 进程内化：cohort=窗内新开 ∪ 窗始 active ∪
// 窗内退出；窗净额=在仓累计+退出 realized；逐日净额=app_config 快照差分
// （accrued_usd 为累计值，库内无逐日流水——同源口径注记保留）。
// 报告面向运营——零票号/内部实现词（结构/退出原因统一映射人话）。
package selfwatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/research/scan"
)

// paperWindowStructure 观测窗结构（=paper_window_report.sh 固定结构）。
const paperWindowStructure = "long_cfd_short_perp"

// structureHuman 结构内部键→运营话术（报告渲染单点；未知键原样透出防臆造）。
func structureHuman(s string) string {
	switch s {
	case "long_cfd_short_perp":
		return "多 CFD 空 perp"
	case "short_cfd_long_perp":
		return "空 CFD 多 perp"
	case "long_spot_short_perp":
		return "多现货空 perp"
	case "cfd_cfd":
		return "双 CFD 息差"
	}
	return s
}

// exitReasonHuman 退出原因内部键→运营话术（日报退出分桶用）。
func exitReasonHuman(r string) string {
	switch r {
	case "normalized":
		return "收益衰减"
	case "adverse":
		return "息差翻负"
	case "data_lost":
		return "数据断供"
	case "age_7d":
		return "满 7 天到期"
	case "zero_accrual_stale":
		return "零计息停滞"
	}
	return r
}

// StageCheck 闸条件单项（✅/❌+数据）。
type StageCheck struct {
	Label string
	OK    bool
	Data  string
}

// DailyReport 日报产物。
type DailyReport struct {
	Body        string
	Stage1OK    bool // 全项 ✅（D-061 条件制判据）→ 附「待决策：Stage 1 可启」+ 单独 info alert
	Stage1Table []StageCheck
	Stage2Table []StageCheck
	LiveOpenN   int // D-102：真金在仓数（>0=组合已启动，Stage 1 段转「在仓」停显就绪表）
	WindowNet   float64
	FailClosed  int
	Stalled     int
}

// evalStage1 只算 Stage 1 就绪表（纯读面，零副作用：不落档/不写 app_config/不
// deliver）——BuildDaily（渲染全表）与 anomalyLoop（D-071：小时级就绪评估只用
// allOK 位）共用单点判定，不复制逻辑。
// 真金在仓（environment='live' 在仓态）→ 组合已启动，「就绪」九项停显
// （返回空表）；计数读败 fail-closed 整报不出——不能证明「无在仓」就不得显就绪面。
func evalStage1(ctx context.Context, d Deps, now time.Time) (rows []StageCheck, since time.Time, windowOK bool, ev *scan.PaperWindowEval, liveOpen int, err error) {
	since, windowOK, err = paperWindowStart(ctx, d)
	if err != nil {
		return nil, since, false, nil, 0, err
	}
	liveOpen, err = d.Store.CountLiveOpenPositions(ctx)
	if err != nil {
		return nil, since, false, nil, 0, fmt.Errorf("stage1 live positions: %w", err)
	}
	if windowOK {
		cohort, cerr := d.Store.PaperWindowCohort(ctx, paperWindowStructure, since)
		if cerr != nil {
			return nil, since, true, nil, liveOpen, fmt.Errorf("stage1 cohort: %w", cerr)
		}
		ev, cerr = d.EvalPaper(ctx, cohort, since, now)
		if cerr != nil {
			return nil, since, true, nil, liveOpen, fmt.Errorf("stage1 eval: %w", cerr)
		}
	}
	if liveOpen > 0 {
		return []StageCheck{}, since, windowOK, ev, liveOpen, nil
	}
	return stage1Checks(ctx, d, since, windowOK, ev, now), since, windowOK, ev, liveOpen, nil
}

// BuildDaily 生成日报（窗起点缺/解析败 → Body 显式报缺，Stage 表全 ❌）。
func BuildDaily(ctx context.Context, d Deps) (*DailyReport, error) {
	now := d.now()
	rows, since, ok, ev, liveOpen, err := evalStage1(ctx, d, now)
	if err != nil {
		return nil, err
	}
	rep := &DailyReport{Stage1Table: rows, LiveOpenN: liveOpen}
	if ok {
		rep.FailClosed, rep.Stalled, rep.WindowNet = ev.FailClosed, ev.Stalled, ev.WindowNet
	}
	rep.Stage2Table = stage2Checks(ctx, d, since, now)
	rep.Stage1OK = allOK(rep.Stage1Table)
	body, err := renderDaily(ctx, d, since, ok, ev, rep, now)
	if err != nil {
		return nil, err
	}
	rep.Body = body
	return rep, nil
}

// paperWindowStart 窗起点=app_config ARB_PAPER_WINDOW_START（迁移 0173 播种）；
// 行缺 → env 兜底 → 双缺 ok=false（fail-closed：不误报）。
func paperWindowStart(ctx context.Context, d Deps) (time.Time, bool, error) {
	v, ok, err := d.Store.AppConfigString(ctx, cfgPaperWindowStart)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("window start: %w", err)
	}
	if !ok || v == "" {
		return time.Time{}, false, nil
	}
	// 秒位可选（播种值 2026-10-03T01:59Z 无秒；RFC3339 全形兼容）。
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		t, perr = time.Parse("2006-01-02T15:04Z07:00", v)
	}
	if perr != nil {
		return time.Time{}, false, fmt.Errorf("window start parse %q: %w", v, perr)
	}
	return t, true, nil
}

// renderDaily 文案渲染（快照测试锚——数字行+条件表全在单函数拼装）。
func renderDaily(ctx context.Context, d Deps, since time.Time, ok bool, ev *scan.PaperWindowEval, rep *DailyReport, now time.Time) (string, error) {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("【ARB 自盯日报】%s BJ\n", now.In(bj).Format("01-02 15:04")))
	if !ok {
		b.WriteString("⚠️ 纸面观测窗起点未配置（ARB_PAPER_WINDOW_START）——日报无法生成\n")
	} else {
		age := now.Sub(since).Hours() / 24
		b.WriteString(fmt.Sprintf("■ 纸面观测窗（%s）｜起点 %s｜已观测 %.1f 天\n",
			structureHuman(paperWindowStructure), since.UTC().Format("2006-01-02T15:04Z"), age))
		b.WriteString(fmt.Sprintf("窗内净额 $%.4f（在仓 $%.4f + 退出 $%.4f）\n",
			ev.WindowNet, ev.NetActive, ev.NetExited))
		dailyLine, err := d.dailyNetLine(ctx, ev.WindowNet, now)
		if err != nil {
			return "", err
		}
		b.WriteString(dailyLine)
		// 行数口径标清——本窗结构行数与全纸面分列行数分开表述，不混。
		b.WriteString(fmt.Sprintf("本窗结构（%s）在仓 %d 行·名义 $%.0f｜演示仓 %d 行｜窗内退出 %d 行\n",
			structureHuman(paperWindowStructure), ev.ActiveN, ev.NotionalUSD, ev.DemoN, ev.ExitedN))
		b.WriteString(fmt.Sprintf("计息缺数据 %d 行｜计息停滞 %d 行（结算水位落后>2 期）\n",
			ev.FailClosed, ev.Stalled))
		if ev.FirstProbeErr != "" {
			b.WriteString("探针错误：" + ev.FirstProbeErr + "\n")
		}
		if len(ev.ActiveByDay) > 0 {
			b.WriteString("在仓水位：" + joinBuckets(ev.ActiveByDay, false) + "\n")
		}
		if len(ev.ExitedByDay) > 0 {
			b.WriteString("退出：" + joinBuckets(ev.ExitedByDay, true) + "\n")
		}
	}
	// 纸面在仓行数/品种数——两轨分列观测项（other=mtm 等兜底列，非零才显）。
	// 明示「全部」域——与上行本窗结构行数区分（多品种/多结构合计）。
	split, err := d.Store.PaperActiveStats(ctx)
	if err != nil {
		return "", fmt.Errorf("paper active stats: %w", err)
	}
	line := fmt.Sprintf("全部纸面在仓：CFD %d 行·%d 品种｜CEX %d 行·%d 品种",
		split.CfdRows, split.CfdSyms, split.CexRows, split.CexSyms)
	if split.OtherRows > 0 {
		line += fmt.Sprintf("｜其他 %d 行·%d 品种", split.OtherRows, split.OtherSyms)
	}
	b.WriteString(line + "\n")
	// CEX 纸面入口拦截分布（环比=累计计数与 app_config 基线差分；
	// sink 未接线则整段省略）。五闸因人话化，余项归「其他」。
	if d.CexSkipStats != nil {
		skipLine, err := d.cexSkipLine(ctx)
		if err != nil {
			return "", err
		}
		b.WriteString(skipLine)
	}
	// 真金在仓 → 段转「在仓（已启动）」，就绪红绿九项停显（组合已启动后
	// 「就绪」语义错位，事故仓历史行永远翻不了绿）；平仓闭合后恢复就绪面。
	// Stage 2 段不动。
	if rep.LiveOpenN > 0 {
		b.WriteString("■ Stage 1 在仓（已启动）\n")
		b.WriteString(fmt.Sprintf("真金在仓 %d 仓——就绪判据停显，平仓后恢复\n", rep.LiveOpenN))
	} else {
		b.WriteString("■ Stage 1 就绪\n")
		writeTable(&b, rep.Stage1Table)
		if rep.Stage1OK {
			b.WriteString("→ 待决策：Stage 1 可启\n")
		}
	}
	b.WriteString("■ Stage 2 条件\n")
	writeTable(&b, rep.Stage2Table)
	return b.String(), nil
}

func writeTable(b *strings.Builder, rows []StageCheck) {
	for _, c := range rows {
		mark := "✅"
		if !c.OK {
			mark = "❌"
		}
		b.WriteString(fmt.Sprintf("%s %s（%s）\n", mark, c.Label, c.Data))
	}
}

func allOK(rows []StageCheck) bool {
	for _, c := range rows {
		if !c.OK {
			return false
		}
	}
	return len(rows) > 0
}
