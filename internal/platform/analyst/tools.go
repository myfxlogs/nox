package analyst

// tools.go 5 工具 + 安全护栏（设计 §2 工具集，全部只读白名单硬编码）。
// 安全单测必覆盖：query_ro 拒 UPDATE/拒多语句/自动 LIMIT/超时；路径 ../ 逃逸拒/白名单外拒/symlink 逃逸拒；rate 桶满拒。
// 拆分子件：tools_query.go（query_ro）、tools_readfile.go（read_doc/read_code+路径安全）、
// tools_kb.go（grep_kb/kb_search）。

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"nox/internal/store/pgstore"
)

// KnRoot 容器内挂载根（设计 §2：./docs:/kn/docs:rw + ./internal:/kn/internal:ro + ...）。
const KnRoot = "/kn"

// Tools 工具执行器（实现 ToolExecutor）。
type Tools struct {
	ReadOnlyDSN string         // NOX_AI_PG_DSN（nox_ro 角色，query_ro 专用）
	Store       *pgstore.Store // get_entity 用主 store（已认证连接，只读查询）
	KnRoot      string         // 文件工具根（默认 /kn，测试可覆盖）
	// R2 返修②：position 派生字段回调（dashboard.Service 注入，复用 enrichOne 同源派生）。
	// nil = 未注入（测试/未接线），get_entity position 返回基础字段不含派生。
	EnrichPosition func(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg) map[string]any
	// ProbeCaller D-100 llm_probe 装配口（dashboard 注入）；nil=未接线→工具回 IsError。
	ProbeCaller ProbeCaller
	// llm_probe 限频计数器（进程级滑动窗：每会话每小时 ≤10，D-100 票面）。
	probeMu     sync.Mutex
	probeWinAt  time.Time
	probeWinCnt int
}

// 工具名常量。
const (
	toolQueryRO   = "query_ro"
	toolGetEntity = "get_entity"
	toolGrepKB    = "grep_kb"
	toolReadDoc   = "read_doc"
	toolReadCode  = "read_code"
	toolLLMProbe  = "llm_probe"
	toolKBSearch  = "kb_search"
)

// Names 返回工具名集。
func (t *Tools) Names() []string {
	return []string{toolQueryRO, toolGetEntity, toolGrepKB, toolReadDoc, toolReadCode, toolLLMProbe, toolKBSearch}
}

// Execute 分派工具调用。
func (t *Tools) Execute(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	switch call.Name {
	case toolQueryRO:
		return t.execQueryRO(ctx, call)
	case toolGetEntity:
		return t.execGetEntity(ctx, call)
	case toolGrepKB:
		return t.execGrepKB(ctx, call)
	case toolReadDoc:
		return t.execReadFile(ctx, call, "docs")
	case toolReadCode:
		return t.execReadFile(ctx, call, "internal", "proto", "cmd")
	case toolLLMProbe:
		return t.execLLMProbe(ctx, call)
	case toolKBSearch:
		return t.execKBSearch(ctx, call)
	default:
		return ToolResult{ToolUseID: call.ID, Content: "未知工具: " + call.Name, IsError: true}, nil, nil
	}
}

// ToolSpecs 返回工具定义（供 client.go MessageRequest.Tools 用）。
func ToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: toolQueryRO, Description: "只读 SQL 查询（仅 SELECT，nox 库，自动 LIMIT 500，5s 超时）", InputSchema: rawJSON(`{"type":"object","properties":{"sql":{"type":"string","description":"SELECT 语句"}},"required":["sql"]}`)},
		{Name: toolGetEntity, Description: "按 ID 拉实体结构化 JSON（position/opportunity/stat-vkey）", InputSchema: rawJSON(`{"type":"object","properties":{"type":{"type":"string","enum":["position","opportunity","stat-vkey"]},"id":{"type":"string"}},"required":["type","id"]}`)},
		{Name: toolGrepKB, Description: "经验库/decisions/docs 关键词检索（grep -rn）", InputSchema: rawJSON(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"子路径如 docs/经验库"}},"required":["pattern"]}`)},
		{Name: toolReadDoc, Description: "读 docs/**/*.md（≤2000 行）", InputSchema: rawJSON(`{"type":"object","properties":{"path":{"type":"string","description":"相对 /kn/docs 的路径"}},"required":["path"]}`)},
		{Name: toolReadCode, Description: "读仓内源码 internal/proto/cmd（≤2000 行）", InputSchema: rawJSON(`{"type":"object","properties":{"path":{"type":"string","description":"相对 /kn 的路径如 internal/analyst/tools.go"}},"required":["path"]}`)},
		{Name: toolLLMProbe, Description: "对当前 LLM 配置做受控判别实验（改一个参数观察输出形态）；用于 13/14 契约的矛盾调查与假设判别", InputSchema: rawJSON(`{"type":"object","properties":{"prompt":{"type":"string","description":"探针输入 ≤500 字符"},"max_tokens":{"type":"integer","description":"256-8192"},"thinking_disabled":{"type":"boolean"}},"required":["prompt"]}`)},
		{Name: toolKBSearch, Description: "经验库条目关键词对拍（条目 keywords vs 文本，高置信命中规则：≥2 词或单 ≥12 字符指纹词）；查败因/症状的既有坑史", InputSchema: rawJSON(`{"type":"object","properties":{"text":{"type":"string","description":"败因签名/症状文本"}},"required":["text"]}`)},
	}
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
