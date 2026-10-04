// probe_report.go —— D-048③：探针平仓（exit_kind='probe_expired'）触发的取证报告。
// 恰好一次：explorer_state 键 probe_report:<posID> 原子认领（PutIfAbsent）——
// 重复事件/并发触发只有首个认领者生成；失败留 failed 标记+warn（不静默重放）。
// 数据源=D-013 账本单源：positions/position_legs/position_cashflows 全确定性拼装，
// LLM 只出「结论 A·B·C + 建议」叙事（无下单/平仓工具——specs 空集）。
package explorer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// probeReportSystemPrompt 取证报告约束（数字禁自算——全部引用输入段原值；
// 结论三选一：A=假设获证据支持 / B=假设被证伪 / C=证据不足或数据缺失）。
const probeReportSystemPrompt = `你是 ARB 套利系统的取证分析员（只读）。输入=探针仓的确定性事实（Go 拼装，禁止改动/重算/自算）。
任务：按假设与证伪标准，对证据下结论。
输出（严格）：一个 fenced 块 ` + "```probe-report" + ` 内含 JSON：
{"conclusion":"A|B|C","analysis":"≤300字证据分析（只引用输入数字）","recommendation":"≤150字建议"}
结论口径：A=假设获证据支持；B=假设被证伪；C=证据不足/数据缺失无法判定。
红线：不预测价格；不给仓位建议；不跨场所借数据；数字只能引用输入段原值。
工作契约：analysis 每条证据判断与 falsify 证伪标准逐条对照（「若 <观测值> 则此判断不成立」式收口）；
recommendation 含「需观察」必须写明触发点（何时/何阈值/触发什么动作），无触发点的等待句删除；
引用数字附口径说明（输入段名/字段名），先自证语义再引用。`

// probeReportStateKey explorer_state 认领键（恰好一次语义载体）。
func probeReportStateKey(posID int64) string {
	return fmt.Sprintf("probe_report:%d", posID)
}

// probeReportOutput LLM 结论块（严格 JSON 解析——不符=LLM 失败路径）。
type probeReportOutput struct {
	Conclusion     string `json:"conclusion"`
	Analysis       string `json:"analysis"`
	Recommendation string `json:"recommendation"`
}

// GenerateProbeReport 生成一份探针取证报告（已认领=幂等跳过）。
// 成功 → docs/研究/probe-<D#>-<date>.md + warn alert（→飞书扇出）；失败 → warn alert。
// 返回 (path, generated, err)：generated=false=跳过（重复触发/非到期平仓）。
func GenerateProbeReport(ctx context.Context, deps Deps, posID int64) (string, bool, error) {
	deps.fill()
	if deps.Store == nil {
		return "", false, fmt.Errorf("explorer: store 未配置")
	}
	now := deps.Now()
	claimed, err := deps.Store.ExplorerStatePutIfAbsent(ctx, probeReportStateKey(posID),
		json.RawMessage(fmt.Sprintf(`{"claimed_at":"%s"}`, now.UTC().Format(time.RFC3339))))
	if err != nil {
		return "", false, err
	}
	if !claimed {
		return "", false, nil // 重复触发——恰好一次锚
	}
	if deps.ResolveClient != nil { // 同 loop：每次触发重解析（DB 优先 env 回落）
		c, rerr := deps.ResolveClient(ctx)
		if rerr != nil {
			return "", false, fmt.Errorf("resolve client: %w", rerr)
		}
		deps.Client = c
	}
	path, err := generateProbeReport(ctx, deps, posID, now)
	if err != nil {
		deps.alert(ctx, "warn", fmt.Sprintf("[探索] 探针报告生成失败 pos=%d: %v", posID, err))
		return "", false, err
	}
	return path, true, nil
}

// generateProbeReport 事实收集 → LLM 结论 → 模板落盘 → 推送。
func generateProbeReport(ctx context.Context, deps Deps, posID int64, now time.Time) (string, error) {
	pos, err := deps.Store.GetProbe(ctx, posID)
	if err != nil {
		return "", fmt.Errorf("probe facts: %w", err)
	}
	if pos.Status != "CLOSED" || pos.ExitKind != pgstore.ExitKindProbeExpired {
		return "", fmt.Errorf("pos=%d 非到期平仓终态（status=%s exit=%s）", posID, pos.Status, pos.ExitKind)
	}
	legs, err := deps.Store.ListPositionLegs(ctx, posID)
	if err != nil {
		return "", fmt.Errorf("legs: %w", err)
	}
	flows, err := deps.Store.ListCashflows(ctx, posID)
	if err != nil {
		return "", fmt.Errorf("cashflows: %w", err)
	}
	prop, perr := deps.Store.ExplorerProbeProposalByPosition(ctx, posID)
	if perr != nil {
		if !errors.Is(perr, pgstore.ErrNoProbeProposal) {
			return "", fmt.Errorf("proposal lookup: %w", perr)
		}
		prop = nil // 手动 OpenProbe（无提案源）——假设段退化为仓 note+decision_ref
	}

	out, err := runProbeReportLLM(ctx, deps, pos, prop, legs, flows)
	if err != nil {
		return "", err
	}
	ref := "no-ref"
	if pos.DecisionRef != nil && *pos.DecisionRef != "" {
		ref = *pos.DecisionRef
	}
	day := now.In(BJTime).Format("2006-01-02")
	name := fmt.Sprintf("probe-%s-%s.md", ref, day)
	body := buildProbeReport(pos, prop, legs, flows, out, now)
	path, err := writeReport(deps.ResearchDir, name, body)
	if err != nil {
		return "", err
	}
	deps.alert(ctx, "warn", fmt.Sprintf("[探针报告] pos=%d %s %s → %s（结论 %s）",
		posID, pos.VenueKey, pos.Symbol, name, out.Conclusion))
	log.Printf("[explorer] probe report: pos=%d path=%s conclusion=%s", posID, path, out.Conclusion)
	return path, nil
}

// runProbeReportLLM 事实段拼装 + 一次 LLM 调用 + 严格解析（失败=err，调用方告警）。
// tools=nil——分析员零执行权（红线条款：LLM 全程不得持有下单/平仓工具）。
func runProbeReportLLM(ctx context.Context, deps Deps, pos *pgstore.ProbeRow,
	prop *pgstore.ProbeProposal, legs []pgstore.PositionLeg, flows []pgstore.Cashflow) (*probeReportOutput, error) {
	if deps.Client == nil {
		return nil, fmt.Errorf("explorer: client 未配置")
	}
	var facts strings.Builder
	fmt.Fprintf(&facts, "## 持仓\npos=%d type=PROBE status=%s exit=%s venue=%s symbol=%s side=%s qty=%.6g\n",
		pos.ID, pos.Status, pos.ExitKind, pos.VenueKey, pos.Symbol, pos.Side, pos.LegQty)
	if pos.NotionalUSD != nil {
		fmt.Fprintf(&facts, "notional_usd=%.4f\n", *pos.NotionalUSD)
	}
	if pos.RealizedPnl != nil {
		fmt.Fprintf(&facts, "realized_pnl=%.6f\n", *pos.RealizedPnl)
	}
	if pos.OpenedAt != nil {
		fmt.Fprintf(&facts, "opened_at=%s\n", pos.OpenedAt.UTC().Format(time.RFC3339))
	}
	if pos.ClosedAt != nil {
		fmt.Fprintf(&facts, "closed_at=%s\n", pos.ClosedAt.UTC().Format(time.RFC3339))
	}
	facts.WriteString("\n## 假设\n")
	if prop != nil {
		fmt.Fprintf(&facts, "purpose=%s\nfalsify=%s\nbasis=%s\nbudget venue=%s canonical=%s side=%s qty=%.6g notional=%.2f expected_cost=%.2f\n",
			prop.Contract.Purpose, prop.Contract.Falsify, prop.Contract.Basis,
			prop.Contract.VenueKey, prop.Contract.Canonical, prop.Contract.Side,
			prop.Contract.Qty, prop.Contract.NotionalUSD, prop.Contract.ExpectedCostUSD)
	} else {
		fmt.Fprintf(&facts, "purpose=%s\n", pos.Note)
	}
	facts.WriteString("\n## 成交记录\n")
	for _, l := range legs {
		fmt.Fprintf(&facts, "leg%d %s %s %s qty=%.6g status=%s", l.LegNo, l.Phase, l.Venue, l.Side, l.Qty, l.Status)
		if l.PriceActual != nil {
			fmt.Fprintf(&facts, " px=%.6g", *l.PriceActual)
		}
		if l.FeeUSD != nil {
			fmt.Fprintf(&facts, " fee_usd=%.4f", *l.FeeUSD)
		}
		fmt.Fprintf(&facts, " order=%s ts=%s\n", l.OrderID, l.Ts.UTC().Format(time.RFC3339))
	}
	facts.WriteString("\n## 现金流\n")
	for _, c := range flows {
		fmt.Fprintf(&facts, "%s %s amount=%.4f ts=%s\n", c.Kind, c.Venue, c.AmountUSD, c.Ts.UTC().Format(time.RFC3339))
	}
	var aiText strings.Builder
	_, err := deps.Run(ctx, deps.Client, nil, nil, probeReportSystemPrompt,
		[]analyst.Message{analyst.TextContent("user", facts.String())},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return nil, fmt.Errorf("analyst run: %w", err)
	}
	return parseProbeReportOutput(aiText.String())
}

// buildProbeReport 报告模板落 markdown（假设 / 成交·现金流·调整记录 / 结论 A·B·C / 建议）。
// 事实段全 Go 渲染（LLM 结论块经严格解析后嵌入，数字面与输入段一致）。
func buildProbeReport(pos *pgstore.ProbeRow, prop *pgstore.ProbeProposal,
	legs []pgstore.PositionLeg, flows []pgstore.Cashflow, out *probeReportOutput, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 探针取证报告 pos #%d（%s）\n\n", pos.ID, now.In(BJTime).Format("2006-01-02"))
	b.WriteString("## 假设\n\n")
	if prop != nil {
		fmt.Fprintf(&b, "- 目的：%s\n- 证伪标准：%s\n- 数据依据：%s\n", prop.Contract.Purpose, prop.Contract.Falsify, prop.Contract.Basis)
		fmt.Fprintf(&b, "- 预算：%s %s %s qty=%.6g 名义≈$%.2f 预期成本≈$%.2f\n\n",
			prop.Contract.VenueKey, prop.Contract.Canonical, prop.Contract.Side,
			prop.Contract.Qty, prop.Contract.NotionalUSD, prop.Contract.ExpectedCostUSD)
	} else {
		fmt.Fprintf(&b, "- 目的（仓注记）：%s\n\n", pos.Note)
	}
	b.WriteString("## 成交记录\n\n| 腿 | 相位 | venue | 方向 | 量 | 成交价 | 费用USD | 状态 | 时刻 |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, l := range legs {
		px, fee := "-", "-"
		if l.PriceActual != nil {
			px = fmt.Sprintf("%.6g", *l.PriceActual)
		}
		if l.FeeUSD != nil {
			fee = fmt.Sprintf("%.4f", *l.FeeUSD)
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %.6g | %s | %s | %s | %s |\n",
			l.LegNo, l.Phase, l.Venue, l.Side, l.Qty, px, fee, l.Status, l.Ts.UTC().Format("01-02 15:04"))
	}
	b.WriteString("\n## 现金流\n\n| 类型 | venue | 金额USD | 时刻 |\n|---|---|---|---|\n")
	for _, c := range flows {
		fmt.Fprintf(&b, "| %s | %s | %.4f | %s |\n", c.Kind, c.Venue, c.AmountUSD, c.Ts.UTC().Format("01-02 15:04"))
	}
	b.WriteString("\n## 调整记录\n\n")
	adj := 0
	for _, l := range legs {
		if l.AttemptNo > 1 || l.Status == "UNKNOWN" {
			fmt.Fprintf(&b, "- 腿%d attempt=%d status=%s\n", l.LegNo, l.AttemptNo, l.Status)
			adj++
		}
	}
	for _, c := range flows {
		switch c.Kind {
		case "commission", "funding", "swap", "price", "interest", "pnl":
		default:
			fmt.Fprintf(&b, "- cashflow kind=%s %s %.4f\n", c.Kind, c.Venue, c.AmountUSD)
			adj++
		}
	}
	if adj == 0 {
		b.WriteString("无（无重试腿/UNKNOWN/调整类现金流）。\n")
	}
	fmt.Fprintf(&b, "\n## 结论 %s\n\n%s\n\n## 建议\n\n%s\n", out.Conclusion, out.Analysis, out.Recommendation)
	return b.String()
}

// parseProbeReportOutput 严格解析 ```probe-report fenced JSON（无块/坏 JSON/结论非 ABC=err）。
func parseProbeReportOutput(text string) (*probeReportOutput, error) {
	start := strings.Index(text, "```probe-report")
	if start < 0 {
		return nil, fmt.Errorf("无 probe-report 结论块")
	}
	rest := text[start+len("```probe-report"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return nil, fmt.Errorf("结论块未闭合（截断）")
	}
	var out probeReportOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[:end])), &out); err != nil {
		return nil, fmt.Errorf("结论 JSON 解析: %w", err)
	}
	switch out.Conclusion {
	case "A", "B", "C":
	default:
		return nil, fmt.Errorf("conclusion 须 A|B|C，得 %q", out.Conclusion)
	}
	if strings.TrimSpace(out.Analysis) == "" {
		return nil, fmt.Errorf("analysis 空")
	}
	return &out, nil
}
