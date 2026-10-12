// report.go —— D-528 S5：中文纯文本渲染（财历日报）。D-120 清退 COT 周报渲染面。
// 时间语义：存储一律 UTC，渲染层转 Asia/Shanghai（设计稿 §5）。
package macro

import (
	"fmt"
	"sort"
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

var importanceCN = map[string]string{"high": "高", "medium": "中", "low": "低", "holiday": "假期"}

// weekdayCN 星期中文（D-169：Go 布局无中文星期 token，Format("周一")=字面透传恒显周一 bug）。
var weekdayCN = [7]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// BJLoc 北京时区（notify 工具渲染共用）。
func BJLoc() *time.Location { return bjLoc }

// ImportanceCN 影响级中文（notify 工具渲染共用）。
func ImportanceCN(imp string) string { return importanceCN[imp] }

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
		tag := local.Format("01-02 ") + weekdayCN[local.Weekday()]
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
