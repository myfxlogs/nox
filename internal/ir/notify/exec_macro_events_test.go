// d528_macro_query_test.go —— D-528 S9：query_macro_events Execute 路径
// mock store 断言中文输出含字段（D-120 清退 query_cot_latest 面）。
package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/analyst"
	"arb/internal/store/pgstore"
)

// fake 补面：D-528 两方法（feishu_query_test.go 的 fakeQueryStore 扩展）。
func (s *fakeQueryStore) MacroEventsBetween(_ context.Context, from, to time.Time) ([]pgstore.MacroEvent, error) {
	return s.macroEvents, nil
}

// compile-time：fake 仍满足扩面后的 FeishuQueryStore。
var _ FeishuQueryStore = (*fakeQueryStore)(nil)

func TestD528_ExecMacroEvents(t *testing.T) {
	et := time.Date(2026, 10, 2, 12, 30, 0, 0, time.UTC)
	store := &fakeQueryStore{
		macroEvents: []pgstore.MacroEvent{{
			Source: "mql5", EventTime: et, Currency: "USD", Name: "Nonfarm Payrolls",
			Importance: "high", Forecast: "170K", Previous: "168K",
		}},
	}
	tools := &feishuQueryTools{store: store}
	res, _, err := tools.Execute(context.Background(), analyst.ToolCall{ID: "t1", Name: "query_macro_events", Input: `{"days_ahead":3}`})
	if err != nil || res.IsError {
		t.Fatalf("execute: err=%v isError=%v content=%s", err, res.IsError, res.Content)
	}
	for _, want := range []string{"未来 3 日宏观事件 1 条", "USD", "高", "Nonfarm Payrolls", "预期 170K", "前值 168K"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("输出缺 %q：%s", want, res.Content)
		}
	}
	// BJ 换算：UTC 12:30 → BJ 20:30 同日。
	if !strings.Contains(res.Content, "10-02 20:30") {
		t.Errorf("BJ 换算缺：%s", res.Content)
	}
	// 无数据路径。
	empty := &feishuQueryTools{store: &fakeQueryStore{}}
	res, _, err = empty.Execute(context.Background(), analyst.ToolCall{ID: "t2", Name: "query_macro_events"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "无宏观事件记录") {
		t.Fatalf("空数据路径: content=%s err=%v", res.Content, err)
	}
}

// 工具注册面：Names 含 query_macro_events（query_cot_latest 已随 D-120 清退）。
func TestD528_ToolNames(t *testing.T) {
	tools := &feishuQueryTools{store: &fakeQueryStore{}}
	has := map[string]bool{}
	for _, n := range tools.Names() {
		has[n] = true
	}
	if !has["query_macro_events"] {
		t.Fatalf("Names 缺 query_macro_events: %v", tools.Names())
	}
}
