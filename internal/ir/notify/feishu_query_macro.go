// feishu_query_macro.go —— D-528 S6：宏观工具位（query_macro_events；D-120 清退 query_cot_latest）。
// 独立文件收编防 feishu_query.go 超 300 行；数据面=FeishuQueryStore 新增两方法（pgstore 适配）。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"nox/internal/data/macro"
	"nox/internal/platform/analyst"
)

// execMacroEvents 未来 N 日宏观事件（中文摘要；days_ahead 缺省 7、夹取 1..30）。
func (t *feishuQueryTools) execMacroEvents(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	days := 7
	var args struct {
		DaysAhead int `json:"days_ahead"`
	}
	if call.Input != "" {
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return analyst.ToolResult{ToolUseID: call.ID, Content: "参数解析失败: days_ahead 须为整数", IsError: true}, nil, nil
		}
		if args.DaysAhead != 0 {
			days = args.DaysAhead
		}
	}
	if days < 1 {
		days = 1
	}
	if days > 30 {
		days = 30
	}
	now := time.Now()
	events, err := t.store.MacroEventsBetween(ctx, now, now.AddDate(0, 0, days))
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查宏观事件失败: " + err.Error(), IsError: true}, nil, nil
	}
	if len(events) == 0 {
		return analyst.ToolResult{ToolUseID: call.ID, Content: fmt.Sprintf("未来 %d 日无宏观事件记录（采集器未落数或日历真空窗）", days)}, nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "未来 %d 日宏观事件 %d 条：", days, len(events))
	for i, e := range events {
		if i == 40 {
			fmt.Fprintf(&b, "\n…（其余 %d 条略）", len(events)-i)
			break
		}
		local := e.EventTime.In(macro.BJLoc())
		detail := e.Forecast
		if e.Actual != "" {
			detail = "实际 " + e.Actual
		} else if detail != "" {
			detail = "预期 " + detail
		}
		if e.Previous != "" {
			if detail != "" {
				detail += "/"
			}
			detail += "前值 " + e.Previous
		}
		fmt.Fprintf(&b, "\n%s %s %s %s", local.Format("01-02 15:04"), e.Currency, macro.ImportanceCN(e.Importance), e.Name)
		if detail != "" {
			b.WriteString("（" + detail + "）")
		}
	}
	return analyst.ToolResult{ToolUseID: call.ID, Content: b.String()}, nil, nil
}
