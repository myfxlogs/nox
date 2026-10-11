// feishu_query_fund.go —— D-201：飞书 NLQ 财务/持仓工具位三枚 + query_ro 长尾层。
// 只读零写；query_ro 委托 analyst.Tools{ReadOnlyDSN 热读}（^SELECT/拒分号/LIMIT500/5s 全继承）。
// 无字段如实报「未记账」——订单无盈亏列不命名 pnl 防 LLM 幻觉。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"arb/internal/analyst"
)

// fundToolSpecs D-201：三枚专用工具 + query_ro（Description 带核心表迷你 schema 摘要）。
func fundToolSpecs() []analyst.ToolSpec {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	limitArg := json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","description":"近 N 单，缺省 10，上限 50"}}}`)
	return []analyst.ToolSpec{
		{Name: "query_strategies_running", Description: "查当前在跑策略：OPEN 双腿仓位（type=策略族）+OPEN 个人持仓（质押/对冲/托管池份额）", InputSchema: empty},
		{Name: "query_finance_summary", Description: "查财务摘要：托管池 BTC 份额、position_daily_income 今日/累计收益、在期 DCI 申购", InputSchema: empty},
		{Name: "query_orders_recent", Description: "查近期 DCI 订单（fin_dci_order 近 N 单 id DESC：状态/venue 回执/proposal 链；无盈亏列——盈亏未记账则如实报）", InputSchema: limitArg},
		{Name: "query_ro", Description: `只读 SQL 长尾查询（仅 SELECT，自动 LIMIT 500，5s 超时）。核心表迷你 schema：
positions(id,opportunity_id,type,status,expected_annual_pct,realized_pnl,opened_at) — type=策略族
personal_positions(id,asset,kind,qty,note,status,opened_at) — 托管池/质押/对冲行，note 自描述
fin_dci_order(id,venue,product_ref,opt_type,strike,invest_ccy,qty,tenor_days,status,response,proposal_id,created_at)
fin_proposal(id,template_id,venue,status,expected_apr,created_at) — status: proposed/executed/expired/declined
position_daily_income(day,energy_trx,voting_trx,interest_usd,pnl_usd)`, InputSchema: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string","description":"SELECT 语句"}},"required":["sql"]}`)},
	}
}

var fundToolNames = []string{"query_strategies_running", "query_finance_summary", "query_orders_recent", "query_ro"}

// execFund D-201：三专用工具+query_ro 分派（Execute 主 switch 外联）。
func (t *feishuQueryTools) execFund(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	switch call.Name {
	case "query_strategies_running":
		return t.execStrategiesRunning(ctx, call)
	case "query_finance_summary":
		return t.execFinanceSummary(ctx, call)
	case "query_orders_recent":
		return t.execOrdersRecent(ctx, call)
	case "query_ro":
		return t.execQueryRO(ctx, call)
	}
	return analyst.ToolResult{ToolUseID: call.ID, Content: "未知工具: " + call.Name, IsError: true}, nil, nil
}

func (t *feishuQueryTools) execStrategiesRunning(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	poss, pers, err := t.store.StrategiesRunning(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查在跑策略失败: " + err.Error(), IsError: true}, nil, nil
	}
	if len(poss) == 0 && len(pers) == 0 {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "当前无 OPEN 仓位与个人持仓"}, nil, nil
	}
	fam := map[string]int{}
	var b strings.Builder
	for i := range poss {
		fam[poss[i].Type]++
	}
	fmt.Fprintf(&b, "双腿仓位 %d 条（按策略族：", len(poss))
	first := true
	for k, n := range fam {
		if !first {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s×%d", k, n)
		first = false
	}
	b.WriteString("）")
	for i := range poss {
		p := &poss[i]
		fmt.Fprintf(&b, "\n#%d %s %s", p.ID, p.Type, p.Status)
		if p.ExpectedAnnualPct != nil {
			fmt.Fprintf(&b, " 预期年化%.1f%%", *p.ExpectedAnnualPct)
		}
		if p.NotionalUSD != nil {
			fmt.Fprintf(&b, " 名义$%.0f", *p.NotionalUSD)
		}
	}
	if len(pers) > 0 {
		fmt.Fprintf(&b, "\n个人持仓 %d 条：", len(pers))
		for i := range pers {
			p := &pers[i]
			fmt.Fprintf(&b, "\n%s/%s", p.Asset, p.Kind)
			if p.Qty != nil {
				fmt.Fprintf(&b, " qty=%g", *p.Qty)
			}
			if p.Note != "" {
				fmt.Fprintf(&b, "（%s）", p.Note)
			}
		}
	}
	return analyst.ToolResult{ToolUseID: call.ID, Content: b.String()}, nil, nil
}

func (t *feishuQueryTools) execFinanceSummary(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	s, err := t.store.FinanceSummary(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查财务摘要失败: " + err.Error(), IsError: true}, nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "托管池 BTC 份额 %.6f", s.CustodyBTCQty)
	fmt.Fprintf(&b, "\n今日收益：能量 %.2f TRX + 投票 %.2f TRX + 息 $%.4f", s.TodayEnergyTRX, s.TodayVotingTRX, s.TodayInterestUSD)
	if s.IncomeDays == 0 {
		b.WriteString("\n累计收益：未记账（position_daily_income 无记录）")
	} else {
		fmt.Fprintf(&b, "\n累计收益（%d 天记账）：息 $%.4f", s.IncomeDays, s.TotalInterestUSD)
		if s.TotalPnlUSD != nil {
			fmt.Fprintf(&b, "，pnl 合计 $%.4f", *s.TotalPnlUSD)
		}
	}
	if len(s.OpenDCI) == 0 {
		b.WriteString("\n在期 DCI：无")
	} else {
		fmt.Fprintf(&b, "\n在期 DCI %d 单：", len(s.OpenDCI))
		for i := range s.OpenDCI {
			o := &s.OpenDCI[i]
			fmt.Fprintf(&b, "\n#%d %s %s腿 qty=%g %s", o.ID, o.ProductRef, o.OptType, o.Qty, o.InvestCcy)
			if o.TenorDays != nil {
				fmt.Fprintf(&b, " %dd", *o.TenorDays)
			}
		}
	}
	return analyst.ToolResult{ToolUseID: call.ID, Content: b.String()}, nil, nil
}

func (t *feishuQueryTools) execOrdersRecent(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	limit := 10
	var args struct {
		Limit int `json:"limit"`
	}
	if call.Input != "" {
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return analyst.ToolResult{ToolUseID: call.ID, Content: "参数解析失败: limit 须为整数", IsError: true}, nil, nil
		}
		if args.Limit > 0 {
			limit = args.Limit
		}
	}
	if limit > 50 {
		limit = 50
	}
	orders, err := t.store.ListDCIOrders(ctx, limit)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查订单失败: " + err.Error(), IsError: true}, nil, nil
	}
	if len(orders) == 0 {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "无 DCI 订单记录"}, nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "近 %d 单 DCI 订单（盈亏未记账——如需推算用 query_ro 查相关表）：", len(orders))
	for i := range orders {
		o := &orders[i]
		fmt.Fprintf(&b, "\n#%d %s %s腿 %s qty=%g %s", o.ID, o.ProductRef, o.OptType, o.Venue, o.Qty, o.InvestCcy)
		if o.ProposalID != nil {
			fmt.Fprintf(&b, " 提案#%d", *o.ProposalID)
		}
		fmt.Fprintf(&b, " 状态=%s", o.Status)
		if o.Response != "" {
			resp := o.Response
			if len(resp) > 120 {
				resp = resp[:120] + "…"
			}
			fmt.Fprintf(&b, " 回执:%s", resp)
		}
	}
	return analyst.ToolResult{ToolUseID: call.ID, Content: b.String()}, nil, nil
}

// execQueryRO D-201②：委托 analyst.Tools{ReadOnlyDSN 热读}——^SELECT/拒分号/
// LIMIT500/5s 超时全继承 analyst 沙箱零放宽；DSN 缺=如实报。
func (t *feishuQueryTools) execQueryRO(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	dsn, err := t.store.AIReadOnlyDSN(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "读 ARB_AI_PG_DSN 失败: " + err.Error(), IsError: true}, nil, nil
	}
	return (&analyst.Tools{ReadOnlyDSN: dsn}).Execute(ctx, call)
}
