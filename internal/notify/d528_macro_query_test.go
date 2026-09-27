// d528_macro_query_test.go —— D-528 S9：query_macro_events/query_cot_latest Execute 路径
// mock store 断言中文输出含字段；fakeQueryStore 补新接口方法（D-528 接口扩面）。
package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// fake 补面：D-528 两方法（feishu_query_test.go 的 fakeQueryStore 扩展）。
func (s *fakeQueryStore) MacroEventsBetween(_ context.Context, from, to time.Time) ([]pgstore.MacroEvent, error) {
	return s.macroEvents, nil
}

func (s *fakeQueryStore) LatestCot(_ context.Context, instrument string, limit int) ([]pgstore.CotPosition, error) {
	return s.cotRows, nil
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

func TestD528_ExecCotLatest(t *testing.T) {
	store := &fakeQueryStore{
		cotRows: []pgstore.CotPosition{{
			ReportDate: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
			Instrument: "6J", Category: "lev_fund", Direction: "LONG",
			NetPosition: 4000, NetChange: 3000,
		}},
	}
	tools := &feishuQueryTools{store: store}
	res, _, err := tools.Execute(context.Background(), analyst.ToolCall{ID: "t1", Name: "query_cot_latest", Input: `{"instrument":"日元"}`})
	if err != nil || res.IsError {
		t.Fatalf("execute: err=%v isError=%v content=%s", err, res.IsError, res.Content)
	}
	for _, want := range []string{"日元（6J）", "截至 2026-09-22", "杠杆基金", "净 4,000", "+3,000"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("输出缺 %q：%s", want, res.Content)
		}
	}
	// 品种缺省→指南路径。
	res, _, err = tools.Execute(context.Background(), analyst.ToolCall{ID: "t2", Name: "query_cot_latest"})
	if err != nil || res.IsError || !strings.Contains(res.Content, "请指定品种") {
		t.Fatalf("指南路径: content=%s err=%v", res.Content, err)
	}
	// 工具注册面：Names 含两新工具。
	names := tools.Names()
	has := map[string]bool{}
	for _, n := range names {
		has[n] = true
	}
	if !has["query_macro_events"] || !has["query_cot_latest"] {
		t.Fatalf("Names 缺新工具: %v", names)
	}
}
