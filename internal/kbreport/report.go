package kbreport

// report.go 报告生成器：Scan → 快照 → RunWithSpecs（AI 只写三节综合）→ 模板拼装
// → 落档 docs/报告/kb-meta-<UTC date>.md + 候选新条目追加 待归纳-分析员.md 收件箱。
// 纪律（D-442 §2/§4）：AI 只读工具白名单、不改统计数字、退役只列不移、候选只进收件箱。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// 默认路径（容器内 /kn/docs 映射宿主 docs/；Deps 字段可覆盖便于测试指 tmpdir）。
const (
	DefaultEntriesDir = "/kn/docs/经验库/条目"
	DefaultReportDir  = "/kn/docs/报告"
	DefaultInboxPath  = "/kn/docs/经验库/待归纳-分析员.md"
)

// Runner RunWithSpecs 等价 seam（测试注入 fake 断言 prompt/输出）。
type Runner func(ctx context.Context, c *analyst.Client, tools analyst.ToolExecutor,
	specs []analyst.ToolSpec, systemPrompt string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error)

// Deps Generate 依赖（wire_kbreport / dashboard RPC 装配）。
type Deps struct {
	Client     *analyst.Client
	Tools      analyst.ToolExecutor
	EntriesDir string           // 空=DefaultEntriesDir
	ReportDir  string           // 空=DefaultReportDir
	InboxPath  string           // 空=DefaultInboxPath
	Run        Runner           // 空=analyst.RunWithSpecs
	Now        func() time.Time // 空=time.Now（UTC date 文件名/陈旧度基准）
}

func (d *Deps) fill() {
	if d.EntriesDir == "" {
		d.EntriesDir = DefaultEntriesDir
	}
	if d.ReportDir == "" {
		d.ReportDir = DefaultReportDir
	}
	if d.InboxPath == "" {
		d.InboxPath = DefaultInboxPath
	}
	if d.Run == nil {
		d.Run = analyst.RunWithSpecs
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// systemPrompt 报告专用系统提示词（防编数/限定三节输出/候选走 fenced 块供程序解析）。
const systemPrompt = `你是 ARB 经验库的元分析员。用户消息里 fenced 块中的统计数字由 Go 确定性计算——禁止改动、重算或复述时写错。
只输出三个二级标题节（标题照抄）：
## 分析
主导失效类别 / 复发模式 / 覆盖缺口 / 趋势判读；每条判读用条目 id 佐证（可用只读工具查条目全文）。
## 退役候选
按 D-440 三判据（被取代 / 系统已移除 / 已证伪）逐行列 ` + "`- id | 判据 | 一句理由`" + `；只列不移（执行权在席位），无候选写「无」。存疑不退。
## 候选新条目
提议的缺口条目草稿标题清单，无则写「无」。本节之后必须紧跟一个 ` + "```kbmeta-candidates" + ` fenced 块，
每行一条 ` + "`标题 | 一句理由`" + `（无则空块）——该块供程序解析进待归纳收件箱，块外文字不进收件箱。
除这三节与候选块外不输出其他内容。`

// reportToolNames 报告专用工具子集（只读白名单：检索/读文档/只读 SQL）。
var reportToolNames = map[string]bool{"grep_kb": true, "read_doc": true, "query_ro": true}

// reportToolSpecs 从 analyst.ToolSpecs 过滤子集。
func reportToolSpecs() []analyst.ToolSpec {
	var out []analyst.ToolSpec
	for _, s := range analyst.ToolSpecs() {
		if reportToolNames[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// candidatesRe 候选新条目 fenced 块（```kbmeta-candidates ... ```）。
var candidatesRe = regexp.MustCompile("(?s)```kbmeta-candidates\\s*\n(.*?)```")

// Generate 生成一期元分析报告。返回落档路径。
func Generate(ctx context.Context, deps Deps) (string, error) {
	deps.fill()
	if deps.Client == nil {
		return "", fmt.Errorf("kbreport: client 未配置")
	}
	now := deps.Now().UTC()
	stats, err := Scan(deps.EntriesDir, now)
	if err != nil {
		return "", fmt.Errorf("scan: %w", err)
	}
	snapshot := RenderMarkdown(stats)

	prompt := fmt.Sprintf("以下是 %s 时点的经验库统计快照（Go 确定性计算）：\n\n```\n%s```\n\n请按系统提示词输出三节分析。", now.Format("2006-01-02"), snapshot)
	var aiText strings.Builder
	_, err = deps.Run(ctx, deps.Client, deps.Tools, reportToolSpecs(), systemPrompt,
		[]analyst.Message{analyst.TextContent("user", prompt)},
		analyst.LoopCallbacks{OnDelta: func(t string) { aiText.WriteString(t) }})
	if err != nil {
		return "", fmt.Errorf("analyst run: %w", err)
	}

	date := now.Format("2006-01-02")
	report := assemble(date, deps.Client.Model, snapshot, aiText.String())
	if err := os.MkdirAll(deps.ReportDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir report dir: %w", err)
	}
	path := filepath.Join(deps.ReportDir, "kb-meta-"+date+".md")
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	// §候选新条目 → 追加待归纳-分析员.md 收件箱（AI 提议≠已验证知识，分诊在席位）。
	if lines := parseCandidates(aiText.String()); len(lines) > 0 {
		var block strings.Builder
		for _, c := range lines {
			fmt.Fprintf(&block, "| %s | source=kb-meta | %s | %s |\n",
				date, cleanCell(c.title), cleanCell(c.reason))
		}
		if err := appendLocked(deps.InboxPath, block.String()); err != nil {
			return path, fmt.Errorf("append inbox: %w", err)
		}
	}
	return path, nil
}

// assemble 模板拼装：AI 标注行 + §统计快照 fenced（AI 不可写）+ AI 三节输出。
func assemble(date, model, snapshot, aiText string) string {
	return fmt.Sprintf(`# KB 元分析报告 %s

> AI 生成（%s），未经独立复审——§退役候选/§候选新条目均须席位复核后执行。

## 统计快照

`+"```\n%s```"+`

%s
`, date, model, snapshot, strings.TrimSpace(aiText))
}

// candidate 候选新条目（标题+理由）。
type candidate struct{ title, reason string }

// parseCandidates 解 ```kbmeta-candidates 块（每行 `标题 | 理由`）。
func parseCandidates(aiText string) []candidate {
	m := candidatesRe.FindStringSubmatch(aiText)
	if m == nil {
		return nil
	}
	var out []candidate
	for _, ln := range strings.Split(m[1], "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		title, reason, _ := strings.Cut(ln, "|")
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		out = append(out, candidate{title: title, reason: strings.TrimSpace(reason)})
	}
	return out
}

// cleanCell 落盘表格单元格清洗（同 dashboard cleanNoteField 零信任口径）。
func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

var inboxMu sync.Mutex

// appendLocked 并发安全追加收件箱（同 AppendAnalystNote appendNote 语义）。
func appendLocked(path, block string) error {
	inboxMu.Lock()
	defer inboxMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}

// ResolveClient DB 优先解析 AI 配置（镜像 dashboard resolveAiConfig 口径：
// DB enabled=false → 拒绝；字段空 → env 回落；api_key 双空 → 错）。
// cmd/core 周报器用（dashboard 内部仍走自己的 resolveAiConfig，不交叉依赖）。
func ResolveClient(ctx context.Context, st *pgstore.Store, envBaseURL, envAPIKey, envModel string) (*analyst.Client, error) {
	baseURL, model, apiKey := envBaseURL, envModel, envAPIKey
	if st != nil {
		if cfg, err := st.GetAiSettings(ctx); err == nil {
			if !cfg.Enabled {
				return nil, fmt.Errorf("分析员已禁用（DB enabled=false）")
			}
			if cfg.BaseURL != "" {
				baseURL = cfg.BaseURL
			}
			if cfg.Model != "" {
				model = cfg.Model
			}
			if cfg.APIKey != "" {
				apiKey = cfg.APIKey
			}
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("分析员未配置（api_key 为空）")
	}
	return analyst.NewClient(baseURL, apiKey, model), nil
}
