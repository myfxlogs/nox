// induct_llm.go —— LLM 聚类→条目草稿段。
// 禁线：产出 status 永=observed（verified 只人工授）；输出必过 JSON schema 闸，
// 违式不落盘只 warn；LLM 只提议草稿不触碰钱径；调用走 analyst 基建（CallLog→fin_llm_call）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"nox/internal/platform/analyst"
)

// llmRunner 可注入测试桩——生产面=analyst.RunWithSpecs 包装。
type llmRunner func(ctx context.Context, system, prompt string) (string, error)

// stringList 宽容反序列化：模型时把清单字段产成单字符串——收 string→单元素数组。
type stringList []string

func (s *stringList) UnmarshalJSON(b []byte) error {
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = []string{one}
	return nil
}

// draftSpec LLM 产出的条目草稿字段（status 不收——程序强写 observed）。
type draftSpec struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Risk      string     `json:"risk"`
	Keywords  stringList `json:"keywords"`
	Scope     stringList `json:"scope"`
	Summary   string     `json:"summary"`
	Facts     string     `json:"facts"`      // 事实/症状节
	RootCause string     `json:"root_cause"` // 根因或策略解释节
	Handling  string     `json:"handling"`   // 处置节
	Boundary  string     `json:"boundary"`   // 适用边界节
	Rows      []int      `json:"rows"`       // 引用源行号（输入序号）
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,80}$`)
	kindSet  = map[string]bool{"pitfall": true, "strategy": true, "invariant": true, "ops": true}
	riskSet  = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	maxDraft = 20 // 单批草稿硬顶（防失控产出）
)

const inductSystem = `你是 NOX 经验库归纳员。输入是未消解的告警/拒绝事件清单（编号+摘要）。
任务：把语义同类的事件聚成一簇，每簇产一条经验条目草稿。
只输出一个 JSON 对象，无其他文字：{"drafts":[{...}]}。
每个 draft 字段：id(ASCII 小写 kebab-case)/kind(pitfall|strategy|invariant|ops)/risk(critical|high|medium|low)/keywords(1-8 个的字符串数组)/scope(字符串数组)/summary(≤120字)/facts/root_cause/handling/boundary/rows(引用的输入编号整数数组)。
不确定的簇不产草稿。禁编造数字/根因；无证据支撑的写「待核」。`

// parseDrafts JSON schema 闸：整体违式 → error（不落盘）；单条违式 → 剔除该条记因。
func parseDrafts(text string, existingIDs map[string]bool) ([]draftSpec, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in output")
	}
	var out struct {
		Drafts []draftSpec `json:"drafts"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	var ok []draftSpec
	for _, d := range out.Drafts {
		if len(ok) >= maxDraft {
			break
		}
		switch {
		case !slugRe.MatchString(d.ID):
			log.Printf("induct: draft %q slug 违式跳过", d.ID)
		case existingIDs[d.ID]:
			log.Printf("induct: draft %s 撞既有条目 id 跳过", d.ID)
		case !kindSet[d.Kind] || !riskSet[d.Risk]:
			log.Printf("induct: draft %s kind/risk 越域跳过", d.ID)
		case len(d.Keywords) == 0 || len(d.Keywords) > 8:
			log.Printf("induct: draft %s keywords 数越界跳过", d.ID)
		case utf8.RuneCountInString(d.Summary) == 0 || utf8.RuneCountInString(d.Summary) > 200:
			log.Printf("induct: draft %s summary 长度越界跳过", d.ID)
		case len(d.Rows) == 0:
			log.Printf("induct: draft %s 零源行引用跳过", d.ID)
		default:
			ok = append(ok, d)
		}
	}
	return ok, nil
}

// renderDraft 按模板 §4.3 六节渲染条目文件（status 强写 observed）。
func renderDraft(d draftSpec, rows []pendingRow, date string) string {
	var b strings.Builder
	b.WriteString("---\nschema_version: 2\nid: " + d.ID + "\nkind: " + d.Kind + "\nrisk: " + d.Risk + "\nstatus: observed\nkeywords:\n")
	for _, k := range d.Keywords {
		b.WriteString("  - " + strings.TrimSpace(k) + "\n")
	}
	b.WriteString("scope:\n")
	for _, s := range d.Scope {
		b.WriteString("  - " + strings.TrimSpace(s) + "\n")
	}
	b.WriteString("summary: " + strings.TrimSpace(d.Summary) + "\nfirst_seen: " + date + "\nlast_verified: " + date + "\nskills: []\nevidence_refs:\n")
	for _, i := range d.Rows {
		if i < 0 || i >= len(rows) {
			continue
		}
		r := rows[i]
		fmt.Fprintf(&b, "  - type: doc\n    ref: %s#L%d\n", r.File, r.Line+1)
	}
	b.WriteString("---\n\n## 事实/症状\n\n" + orNA(d.Facts) + "\n\n## 根因或策略解释\n\n" + orNA(d.RootCause) +
		"\n\n## 处置\n\n" + orNA(d.Handling) + "\n\n## 证据\n\n- check: 待核（自动归纳草稿，人工验证后翻 verified）\n- result: 源行见 evidence_refs\n- ref: induct 管线\n" +
		"\n\n## 反证条件\n\n- check: 人工复核\n- falsifies_when: 草稿与实据不符\n\n## 适用边界\n\n" + orNA(d.Boundary) + "\n")
	return b.String()
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "待核"
	}
	return strings.TrimSpace(s)
}

// inductPrompt 构造 LLM 输入：编号+源行摘要清单。
func inductPrompt(rows []pendingRow) string {
	var b strings.Builder
	b.WriteString("未消解事件清单：\n\n")
	for i, r := range rows {
		sum := r.Summary
		if utf8.RuneCountInString(sum) > 200 {
			sum = string([]rune(sum)[:200]) + "…"
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i, r.Source, sum)
	}
	return b.String()
}

// noTools 归纳面零工具执行器（模型违发 tool_use 时返回错误结果，禁 nil panic）。
type noTools struct{}

func (noTools) Execute(context.Context, analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	return analyst.ToolResult{Content: "归纳面无工具", IsError: true}, nil, nil
}
func (noTools) Names() []string { return nil }

// defaultLLMRunner 生产径：analyst.RunWithSpecs 零工具集（纯文本→JSON，无工具回路）。
func defaultLLMRunner(c *analyst.Client) llmRunner {
	return func(ctx context.Context, system, prompt string) (string, error) {
		var out strings.Builder
		_, err := analyst.RunWithSpecs(ctx, c, noTools{}, nil, system,
			[]analyst.Message{analyst.TextContent("user", prompt)},
			analyst.LoopCallbacks{OnDelta: func(t string) { out.WriteString(t) }})
		return out.String(), err
	}
}
