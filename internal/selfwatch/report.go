// report.go —— D-033①②：每日 08:00 BJ paper 窗日报 + Stage 闸就绪表。
// 口径=scripts/paper_window_report.sh 进程内化：cohort=窗内新开 ∪ 窗始 active ∪
// 窗内退出；窗净额=在仓累计+退出 realized；逐日净额=app_config 快照差分
// （accrued_usd 为累计值，库内无逐日流水——同源口径注记保留）。
package selfwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"arb/internal/scan"
)

// paperWindowStructure 观测窗结构（=paper_window_report.sh 固定结构）。
const paperWindowStructure = "long_cfd_short_perp"

// StageCheck 闸条件单项（✅/❌+数据）。
type StageCheck struct {
	Label string
	OK    bool
	Data  string
}

// DailyReport 日报产物。
type DailyReport struct {
	Body        string
	Stage1OK    bool // 全项 ✅ → 附「待决策：Stage 1 可启」+ 单独 info alert
	Stage1Table []StageCheck
	Stage2Table []StageCheck
	WindowNet   float64
	FailClosed  int
	Stalled     int
}

// BuildDaily 生成日报（窗起点缺/解析败 → Body 显式报缺，Stage 表全 ❌）。
func BuildDaily(ctx context.Context, d Deps) (*DailyReport, error) {
	now := d.now()
	since, ok, err := paperWindowStart(ctx, d)
	if err != nil {
		return nil, err
	}
	rep := &DailyReport{}
	var ev *scan.PaperWindowEval
	if ok {
		rows, cerr := d.Store.PaperWindowCohort(ctx, paperWindowStructure, since)
		if cerr != nil {
			return nil, fmt.Errorf("daily report cohort: %w", cerr)
		}
		ev, cerr = d.EvalPaper(ctx, rows, since, now)
		if cerr != nil {
			return nil, fmt.Errorf("daily report eval: %w", cerr)
		}
		rep.FailClosed, rep.Stalled, rep.WindowNet = ev.FailClosed, ev.Stalled, ev.WindowNet
	}
	rep.Stage1Table = stage1Checks(ctx, d, since, ok, ev, now)
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
	if !ok {
		v = os.Getenv(cfgPaperWindowStart)
	}
	if v == "" {
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
	b.WriteString(fmt.Sprintf("【ARB 自盯日报】%s BJ\n", now.In(bj).Format("01-02 08:00")))
	if !ok {
		b.WriteString("⚠️ ARB_PAPER_WINDOW_START 未配置——paper 窗日报不可生成（fail-closed）\n")
	} else {
		age := now.Sub(since).Hours() / 24
		b.WriteString(fmt.Sprintf("■ paper 窗 %s｜起点 %s｜窗龄 %.1fd\n",
			paperWindowStructure, since.UTC().Format("2006-01-02T15:04Z"), age))
		b.WriteString(fmt.Sprintf("窗内净额 $%.4f（在仓 $%.4f + 退出 $%.4f）\n",
			ev.WindowNet, ev.NetActive, ev.NetExited))
		dailyLine, err := d.dailyNetLine(ctx, ev.WindowNet, now)
		if err != nil {
			return "", err
		}
		b.WriteString(dailyLine)
		b.WriteString(fmt.Sprintf("在仓 %d 行·名义 $%.0f｜demo %d｜窗内退出 %d 行\n",
			ev.ActiveN, ev.NotionalUSD, ev.DemoN, ev.ExitedN))
		b.WriteString(fmt.Sprintf("fail-closed %d 行｜accrue 停滞 %d 行（水位落后>2 结算窗）\n",
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
	// D-050 ②：纸面在仓行数/品种数——全 active 观测项（不分 structure，无阈值）。
	rows, symbols, err := d.Store.PaperActiveStats(ctx)
	if err != nil {
		return "", fmt.Errorf("paper active stats: %w", err)
	}
	b.WriteString(fmt.Sprintf("纸面在仓 %d 行·%d 品种\n", rows, symbols))
	b.WriteString("■ Stage 1 就绪\n")
	writeTable(&b, rep.Stage1Table)
	if rep.Stage1OK {
		b.WriteString("→ 待决策：Stage 1 可启\n")
	}
	b.WriteString("■ Stage 2 条件\n")
	writeTable(&b, rep.Stage2Table)
	return b.String(), nil
}

// dailyNetLine 逐日净额行：app_config 快照差分（prev 缺=首日基线记 0 增量并播种）。
func (d Deps) dailyNetLine(ctx context.Context, windowNet float64, now time.Time) (string, error) {
	day := now.In(bj).Format("2006-01-02")
	hist := map[string]float64{}
	if v, ok, err := d.Store.AppConfigString(ctx, cfgPaperNetHist); err != nil {
		return "", fmt.Errorf("net hist load: %w", err)
	} else if ok {
		if jerr := json.Unmarshal([]byte(v), &hist); jerr != nil {
			return "", fmt.Errorf("net hist parse: %w", jerr)
		}
	}
	hist[day] = windowNet
	// 滚动保留近 60 天（表行不无限长）。
	if len(hist) > 60 {
		keys := make([]string, 0, len(hist))
		for k := range hist {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-60] {
			delete(hist, k)
		}
	}
	buf, err := json.Marshal(hist)
	if err != nil {
		return "", fmt.Errorf("net hist marshal: %w", err)
	}
	if err := d.Store.UpsertAppConfig(ctx, cfgPaperNetHist, string(buf), "D-033 逐日净额快照（内部状态键）"); err != nil {
		return "", fmt.Errorf("net hist save: %w", err)
	}
	keys := make([]string, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	var prev float64
	for i, k := range keys {
		dv := hist[k]
		if i == 0 {
			parts = append(parts, fmt.Sprintf("%s $%.4f", k[5:], dv))
		} else {
			parts = append(parts, fmt.Sprintf("%s %+.4f", k[5:], dv-prev))
		}
		prev = dv
	}
	return "逐日净额：" + strings.Join(parts, "｜") + "\n", nil
}

// joinBuckets 日分桶行渲染（"MM-DD ×N($sum)"）。
func joinBuckets(bs []scan.PaperDayBucket, withReasons bool) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		s := fmt.Sprintf("%s ×%d($%.4f)", b.Day, b.Rows, b.Sum)
		if withReasons && len(b.Reasons) > 0 {
			s += "[" + strings.Join(b.Reasons, ",") + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "｜")
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
