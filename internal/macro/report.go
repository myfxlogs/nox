// report.go —— D-528 S5：中文纯文本渲染（COT 周报+财历日报）。
// 时间语义：存储一律 UTC，渲染层转 Asia/Shanghai（设计稿 §5）。
// v1 原始字段边界：pct_index/momentum/conviction 暂空渲染为 "-"（合成后续票）。
package macro

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// bjLoc 北京时区（渲染与调度共用；tzdata 缺失回退 FixedZone+8）。
var bjLoc = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}()

var sectorOrder = []struct{ key, cn string }{
	{"FX", "外汇"}, {"EQ", "股指"}, {"CMDTY", "大宗"}, {"BOND", "债券"}, {"CRYPTO", "加密"},
}

var categoryCN = map[string]string{
	"lev_fund": "杠杆基金", "asset_mgr": "资管", "dealer": "交易商",
	"managed_money": "管理基金", "prod_merc": "生产商贸", "swap": "掉期商",
	"noncomm": "非商业", "comm": "商业",
}

var dirCN = map[string]string{"LONG": "多", "SHORT": "空", "FLAT": "平"}

var importanceCN = map[string]string{"high": "高", "medium": "中", "low": "低", "holiday": "假期"}

// BJLoc 北京时区（notify 工具渲染共用）。
func BJLoc() *time.Location { return bjLoc }

// ImportanceCN 影响级中文（notify 工具渲染共用）。
func ImportanceCN(imp string) string { return importanceCN[imp] }

// CnSymbol 品种中文名（notify 工具渲染共用）。
func CnSymbol(symbol string) string { return cnName(symbol) }

// CategoryCN COT 类别中文（notify 工具渲染共用）。
func CategoryCN(cat string) string { return categoryCN[cat] }

// CommaInt 千分位整数（notify 工具渲染共用）。
func CommaInt(v int64) string { return comma(v) }

// SignedInt 带符号千分位（notify 工具渲染共用）。
func SignedInt(v int64) string { return signed(v) }

// InstrumentGuide 全品种指南（品种别名无法解析时给 LLM 的可选集）。
func InstrumentGuide() string {
	var b strings.Builder
	for _, c := range cotContracts {
		fmt.Fprintf(&b, "%s(%s) ", cnName(c.Symbol), c.Symbol)
	}
	return strings.TrimSpace(b.String())
}

// symbolSector 品种→分区（渲染分组用）。
func symbolSector(symbol string) string {
	for _, c := range cotContracts {
		if c.Symbol == symbol {
			return c.Sector
		}
	}
	return ""
}

// RenderCotCN COT 周报：分区头+每行 `品种(类别) 方向 | 净持仓 | 周变动 | 百分位 | 动量 | 信念分`。
func RenderCotCN(rows []pgstore.CotPosition) string {
	if len(rows) == 0 {
		return "CFTC COT 周报：暂无数据"
	}
	latest := rows[0].ReportDate
	bySector := map[string][]pgstore.CotPosition{}
	for _, r := range rows {
		if r.ReportDate.After(latest) {
			latest = r.ReportDate
		}
		bySector[symbolSector(r.Instrument)] = append(bySector[symbolSector(r.Instrument)], r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【CFTC COT 持仓周报（截至 %s）】", latest.Format("2006-01-02"))
	for _, sec := range sectorOrder {
		rows := bySector[sec.key]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Instrument != rows[j].Instrument {
				return rows[i].Instrument < rows[j].Instrument
			}
			return rows[i].Category < rows[j].Category
		})
		fmt.Fprintf(&b, "\n◆ %s", sec.cn)
		for _, r := range rows {
			pct, mom, conv := "-", "-", "-"
			if r.PctIndex != nil {
				pct = fmt.Sprintf("%.0f%%", *r.PctIndex)
			}
			if r.Momentum != "" {
				mom = r.Momentum
			}
			if r.Conviction != nil {
				conv = fmt.Sprintf("%.2f", *r.Conviction)
			}
			fmt.Fprintf(&b, "\n%s %s %s | 净持仓 %s | 周变动 %s | 百分位 %s | 动量 %s | 信念分 %s",
				cnName(r.Instrument), categoryCN[r.Category], dirCN[r.Direction],
				comma(r.NetPosition), signed(r.NetChange), pct, mom, conv)
		}
	}
	return b.String()
}

// RenderCalendarCN 财历日报：now 起 48h（BJ 当日+次日）高/中影响事件，时间转 BJ。
func RenderCalendarCN(events []pgstore.MacroEvent, now time.Time) string {
	bjNow := now.In(bjLoc)
	dayStart := time.Date(bjNow.Year(), bjNow.Month(), bjNow.Day(), 0, 0, 0, 0, bjLoc)
	windowEnd := dayStart.AddDate(0, 0, 2)
	var picked []pgstore.MacroEvent
	for _, e := range events {
		if e.Importance != "high" && e.Importance != "medium" {
			continue
		}
		local := e.EventTime.In(bjLoc)
		if !local.Before(dayStart) && local.Before(windowEnd) {
			picked = append(picked, e)
		}
	}
	if len(picked) == 0 {
		return "【财经日历（今日+明日，北京时间）】未来 48 小时无高/中影响事件"
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].EventTime.Before(picked[j].EventTime) })
	var b strings.Builder
	b.WriteString("【财经日历（今日+明日，北京时间）】")
	dayTag := ""
	for _, e := range picked {
		local := e.EventTime.In(bjLoc)
		tag := local.Format("01-02 周一")
		if tag != dayTag {
			dayTag = tag
			fmt.Fprintf(&b, "\n— %s —", tag)
		}
		detail := fmt.Sprintf("预期 %s / 前值 %s", e.Forecast, e.Previous)
		if e.Actual != "" {
			detail = fmt.Sprintf("实际 %s（预期 %s / 前值 %s）", e.Actual, e.Forecast, e.Previous)
		}
		if e.Forecast == "" && e.Previous == "" && e.Actual == "" {
			detail = ""
		} else if e.Forecast == "" && e.Previous == "" {
			detail = fmt.Sprintf("实际 %s", e.Actual)
		}
		if detail != "" {
			detail = "（" + detail + "）"
		}
		fmt.Fprintf(&b, "\n%s %s %s %s%s %s",
			local.Format("01-02 15:04"), e.Currency, importanceCN[e.Importance], e.Name, detail, sourceTag(e.Source))
	}
	return b.String()
}

// sourceTag 数据源标注（多源同表时人读可辨；mql5 唯一源时为空串降噪）。
func sourceTag(source string) string {
	if source == "mql5" {
		return ""
	}
	return "[" + source + "]"
}

// comma 千分位（负数含符号）。
func comma(v int64) string {
	s := strconv.FormatInt(abs(v), 10)
	if len(s) > 3 {
		var parts []string
		for len(s) > 3 {
			parts = append([]string{s[len(s)-3:]}, parts...)
			s = s[:len(s)-3]
		}
		parts = append([]string{s}, parts...)
		s = strings.Join(parts, ",")
	}
	if v < 0 {
		return "-" + s
	}
	return s
}

// signed 千分位+显式符号（周变动列用）。
func signed(v int64) string {
	if v > 0 {
		return "+" + comma(v)
	}
	return comma(v)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
