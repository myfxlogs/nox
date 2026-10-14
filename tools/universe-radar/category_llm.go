package main

// 品类预审 LLM 段：memo 生成/schema 闸/生产径 runner。
// 禁线：LLM 只产 memo 不碰批准键；调用走 analyst 基建（CallLog→fin_llm_call role=category_memo）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"nox/internal/platform/analyst"
)

// catMemo LLM 预审：品类信号→{family_fit, params_proto, memo}；违式/败→novel+空 memo 占位。
// schema 闸同 induct 式：family_fit ∈ 三域、memo ≤2000 字，违式降级不拒行。
func catMemo(ctx context.Context, llm catLLM, sig CategorySignal) (string, string, string) {
	text, err := llm(ctx, catMemoSystem, catMemoPrompt(sig))
	if err != nil {
		log.Printf("[universe-radar] cat memo %s llm: %v", sig.CategoryKey, err)
		return "novel", "", ""
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		log.Printf("[universe-radar] cat memo %s 无 JSON——占位", sig.CategoryKey)
		return "novel", "", ""
	}
	var out struct {
		FamilyFit   string          `json:"family_fit"`
		ParamsProto json.RawMessage `json:"params_proto"`
		Memo        string          `json:"memo"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		log.Printf("[universe-radar] cat memo %s 解析败——占位: %v", sig.CategoryKey, err)
		return "novel", "", ""
	}
	if !map[string]bool{"generic_yield": true, "novel": true, "none": true}[out.FamilyFit] {
		out.FamilyFit = "novel"
	}
	if utf8.RuneCountInString(out.Memo) > 2000 {
		out.Memo = string([]rune(out.Memo)[:2000])
	}
	proto := ""
	if len(out.ParamsProto) > 0 && string(out.ParamsProto) != "null" {
		proto = string(out.ParamsProto)
	}
	return out.FamilyFit, strings.TrimSpace(out.Memo), proto
}

const catMemoSystem = `你是 NOX 套利系统的新品类预审员。输入是一条 venue 目录内新品类信号
（venue/仪器类型/形态键/行数/示例符号）。任务：产预审备忘录，供决策席裁决。
只输出一个 JSON 对象，无其他文字：{"family_fit":"generic_yield|novel|none",
"params_proto":{...},"memo":"≤800字"}。
family_fit 判据：generic_yield=契约型固定收益族（申购/赎回、APR×tenor 可评）；
novel=新机制（非固定收益形态，装不进通用族须立施工票）；none=非收益品类（不值得追）。
params_proto=若判 generic_yield，列所需参面草案键（venue/product_id/ccy/tenor_days/apr/
redeem_type 等），其他判定可为空对象。memo=品类本质一句话+风险疑点+建议处置（批/立票/弃）。
你只产备忘录——批准键不在你手里，不给出任何执行指令。禁编造 venue 未给的数字。`

func catMemoPrompt(sig CategorySignal) string {
	return fmt.Sprintf("品类信号：category_key=%s venue=%s inst_type=%s shape=%s 行数=%d 示例=%s 详情=%s",
		sig.CategoryKey, sig.Venue, sig.InstType, sig.Shape, sig.Count, sig.Sample, sig.Detail)
}

// catLLMRunner 生产径包装（analyst.RunWithSpecs 零工具集，同 induct 式）。
func catLLMRunner(c *analyst.Client) catLLM {
	return func(ctx context.Context, system, prompt string) (string, error) {
		var out strings.Builder
		_, err := analyst.RunWithSpecs(ctx, c, noToolExec{}, nil, system,
			[]analyst.Message{analyst.TextContent("user", prompt)},
			analyst.LoopCallbacks{OnDelta: func(t string) { out.WriteString(t) }})
		return out.String(), err
	}
}

// noToolExec 品类面零工具执行器（模型违发 tool_use 时返回错误结果）。
type noToolExec struct{}

func (noToolExec) Execute(context.Context, analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	return analyst.ToolResult{Content: "品类预审面无工具", IsError: true}, nil, nil
}
func (noToolExec) Names() []string { return nil }

// Categories 新品类侦测（S1 a 级源）：exchangeInfo 中收轴集外的
// (contractType,quoteAsset) 对→品类信号——月度交割/币本位等已知但未收品类首次各产一行。
func (b binance) Categories(ctx context.Context) ([]CategorySignal, error) {
	ei, err := b.exchangeInfoCached(ctx)
	if err != nil {
		return nil, err
	}
	ca := newCatAgg()
	for _, s := range ei.Symbols {
		perp := strings.Contains(s.ContractType, "PERPETUAL")
		quarter := strings.Contains(s.ContractType, "QUARTER")
		if s.QuoteAsset == "USDT" && (perp || quarter) {
			continue // 在收轴集
		}
		key := s.ContractType + "|" + s.QuoteAsset
		ca.add(key, s.Symbol)
	}
	var out []CategorySignal
	for _, key := range ca.order {
		parts := strings.SplitN(key, "|", 2)
		a := ca.m[key]
		out = append(out, CategorySignal{
			CategoryKey: "binance|" + key,
			Venue:       "binance", InstType: parts[0], Shape: parts[1],
			Count: a.n, Sample: strings.Join(a.samples, ","),
			Detail: fmt.Sprintf("exchangeInfo contractType=%s quoteAsset=%s 收轴集外", parts[0], parts[1]),
		})
	}
	return out, nil
}

// Categories 新品类侦测（S1 a 级源）：FUTURES 载荷内白名单外中段（币本位等）
// + OPTION instType 存在性。instType 全集无枚举端点——全新 instType 不可侦为
// b 级公告源缺位，如实记（设计稿 §2 决策点 D# 待裁）。
func (o okx) Categories(ctx context.Context) ([]CategorySignal, error) {
	snap, err := o.swapCached(ctx)
	if err != nil {
		return nil, err
	}
	ca := newCatAgg()
	for _, d := range snap.Instruments {
		parts := strings.Split(d.InstID, "-")
		if d.InstType != "FUTURES" || len(parts) != 3 || okxLinearMid[parts[1]] {
			continue
		}
		key := "FUTURES|" + parts[1]
		ca.add(key, d.InstID)
	}
	var out []CategorySignal
	for _, key := range ca.order {
		mid := strings.SplitN(key, "|", 2)[1]
		a := ca.m[key]
		out = append(out, CategorySignal{
			CategoryKey: "okx|" + key,
			Venue:       "okx", InstType: "FUTURES", Shape: mid,
			Count: a.n, Sample: strings.Join(a.samples, ","),
			Detail: fmt.Sprintf("FUTURES 中段=%s（币本位逆向——白名单外）", mid),
		})
	}
	if len(snap.Options) > 0 {
		var samples []string
		for i, d := range snap.Options {
			if i >= 3 {
				break
			}
			samples = append(samples, d.InstID)
		}
		out = append(out, CategorySignal{
			CategoryKey: "okx|OPTION",
			Venue:       "okx", InstType: "OPTION", Shape: "option",
			Count: len(snap.Options), Sample: strings.Join(samples, ","),
			Detail: "OPTION instType 在架（greeks/premium 轴——设计钦定不收入宇宙）",
		})
	}
	return out, nil
}

// Categories 新品类侦测（S1 a 级源）：contracts 载荷内收轴集外形态键——
// 收轴={USDT perp, USDT_D 交割}；其余形态（币本位/外币结算/新结构名）产信号。
func (g gate) Categories(ctx context.Context) ([]CategorySignal, error) {
	if g.hc == nil || g.base == "" {
		return nil, fmt.Errorf("REST 面未接线")
	}
	var arr []struct {
		Name string `json:"name"`
	}
	if err := getJSON(ctx, g.hc, g.base+"/api/v4/futures/usdt/contracts", &arr); err != nil {
		return nil, err
	}
	ca := newCatAgg()
	for _, c := range arr {
		shape := gateShape(c.Name)
		if shape == "USDT" || shape == "USDT_D" {
			continue // 在收轴集
		}
		ca.add(shape, c.Name)
	}
	var out []CategorySignal
	for _, shape := range ca.order {
		a := ca.m[shape]
		out = append(out, CategorySignal{
			CategoryKey: "gate|shape|" + shape,
			Venue:       "gate", InstType: "futures", Shape: shape,
			Count: a.n, Sample: strings.Join(a.samples, ","),
			Detail: fmt.Sprintf("contracts 形态键=%s（收轴 USDT/USDT_D 外）", shape),
		})
	}
	return out, nil
}

// gateShape 合约名→形态键：首个 _ 后段，数字串归一 D
// （BTC_USDT→USDT、BTC_USDT_20241227→USDT_D、BTC_USD→USD、BTC_USDT_2024W01→USDT_DW）。
func gateShape(name string) string {
	i := strings.Index(name, "_")
	if i < 0 {
		return name
	}
	var b strings.Builder
	for _, part := range strings.Split(name[i+1:], "_") {
		if allDigits(part) {
			b.WriteString("D")
		} else {
			b.WriteString(part)
		}
		b.WriteString("_")
	}
	return strings.TrimSuffix(b.String(), "_")
}
