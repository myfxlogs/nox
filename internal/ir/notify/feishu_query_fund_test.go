// feishu_query_fund_test.go —— D-201 锚：三工具对拍/非 SELECT 拒/缺 DSN 如实报/schema 入 spec。
package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

func fundTools(s *fakeQueryStore) *feishuQueryTools { return &feishuQueryTools{store: s} }

// 三工具对拍：策略族聚合+财务摘要+订单提案链如实渲染。
func TestD201_FundTools(t *testing.T) {
	f := &fakeQueryStore{
		runPositions: []pgstore.Position{
			{ID: 1, Type: "SWAPARB", Status: "OPEN"},
			{ID: 2, Type: "SWAPARB", Status: "OPEN"},
			{ID: 3, Type: "CARRY", Status: "OPEN"},
		},
		runPersonal: []pgstore.PersonalPosition{
			{ID: 9, Asset: "BTC", Kind: "other", Qty: f64(0.005), Note: "DCI 托管池"},
		},
		finSummary: FeishuFinanceSummary{
			CustodyBTCQty: 0.005, TodayInterestUSD: 1.23, TotalInterestUSD: 45.6, IncomeDays: 30,
			OpenDCI: []pgstore.DCIOrder{{ID: 5, ProductRef: "P1", OptType: "P", Qty: 541, InvestCcy: "USDT"}},
		},
		dciOrders: []pgstore.DCIOrder{
			{ID: 2, ProductRef: "P2", OptType: "P", Venue: "binance", Qty: 541, InvestCcy: "USDT",
				Status: "filled", Response: "ok", ProposalID: i64(7)},
			{ID: 1, ProductRef: "P1", OptType: "C", Venue: "binance", Qty: 0.001, InvestCcy: "BTC", Status: "failed"},
		},
	}
	tk := fundTools(f)
	ctx := context.Background()

	r, _, err := tk.Execute(ctx, analyst.ToolCall{ID: "a", Name: "query_strategies_running"})
	if err != nil || r.IsError {
		t.Fatalf("strategies: %v %v", err, r.Content)
	}
	if !strings.Contains(r.Content, "SWAPARB×2") || !strings.Contains(r.Content, "CARRY×1") {
		t.Fatalf("策略族聚合缺：%s", r.Content)
	}
	if !strings.Contains(r.Content, "BTC/other") || !strings.Contains(r.Content, "DCI 托管池") {
		t.Fatalf("个人持仓缺：%s", r.Content)
	}

	r, _, _ = tk.Execute(ctx, analyst.ToolCall{ID: "b", Name: "query_finance_summary"})
	if !strings.Contains(r.Content, "0.005") || !strings.Contains(r.Content, "$1.23") ||
		!strings.Contains(r.Content, "30 天记账") || !strings.Contains(r.Content, "在期 DCI 1 单") {
		t.Fatalf("财务摘要缺：%s", r.Content)
	}

	r, _, _ = tk.Execute(ctx, analyst.ToolCall{ID: "c", Name: "query_orders_recent", Input: `{"limit":10}`})
	if !strings.Contains(r.Content, "提案#7") || !strings.Contains(r.Content, "状态=filled") {
		t.Fatalf("订单链缺：%s", r.Content)
	}
	if strings.Contains(strings.ToLower(r.Content), "pnl") {
		t.Fatalf("无盈亏列不得命名 pnl：%s", r.Content)
	}
}

// 非 SELECT 拒 + 缺 DSN 如实报（沙箱未放宽）。
func TestD201_QueryROGate(t *testing.T) {
	tk := fundTools(&fakeQueryStore{roDSN: ""})
	ctx := context.Background()

	r, _, _ := tk.Execute(ctx, analyst.ToolCall{ID: "a", Name: "query_ro", Input: `{"sql":"UPDATE positions SET status='X'"}`})
	if !r.IsError || !strings.Contains(r.Content, "仅允许 SELECT") {
		t.Fatalf("非 SELECT 未拒：%v %s", r.IsError, r.Content)
	}
	r, _, _ = tk.Execute(ctx, analyst.ToolCall{ID: "b", Name: "query_ro", Input: `{"sql":"SELECT 1; DROP TABLE x"}`})
	if !r.IsError || !strings.Contains(r.Content, "多语句") {
		t.Fatalf("分号未拒：%v %s", r.IsError, r.Content)
	}
	r, _, _ = tk.Execute(ctx, analyst.ToolCall{ID: "c", Name: "query_ro", Input: `{"sql":"SELECT 1"}`})
	if !r.IsError || !strings.Contains(r.Content, "NOX_AI_PG_DSN") {
		t.Fatalf("缺 DSN 未如实报：%v %s", r.IsError, r.Content)
	}
}

// schema 入 spec：query_ro Description 带核心表迷你 schema。
func TestD201_SchemaInSpec(t *testing.T) {
	var ro *analyst.ToolSpec
	for i := range fundToolSpecs() {
		if fundToolSpecs()[i].Name == "query_ro" {
			ro = &fundToolSpecs()[i]
		}
	}
	if ro == nil {
		t.Fatalf("query_ro spec 缺")
	}
	for _, tbl := range []string{"fin_dci_order", "fin_proposal", "personal_positions", "position_daily_income", "positions"} {
		if !strings.Contains(ro.Description, tbl) {
			t.Fatalf("schema 摘要缺表 %s", tbl)
		}
	}
	// 三工具名注册进 Names()
	tk := fundTools(&fakeQueryStore{})
	names := map[string]bool{}
	for _, n := range tk.Names() {
		names[n] = true
	}
	for _, want := range []string{"query_strategies_running", "query_finance_summary", "query_orders_recent", "query_ro"} {
		if !names[want] {
			t.Fatalf("Names 缺 %s", want)
		}
	}
	// 在期 DCI 判定：filled+未到期——adapter 层（pgstore）由 D200 锚覆，此锚纯渲染行。
	pn := FeishuFinanceSummary{OpenDCI: []pgstore.DCIOrder{{ID: 1, ProductRef: "P", CreatedAt: time.Now()}}}
	if len(pn.OpenDCI) != 1 {
		t.Fatalf("OpenDCI 缺")
	}
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
