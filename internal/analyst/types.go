// Package analyst 前端分析员 agent（D-091）：Anthropic 兼容 messages API 客户端 +
// 工具循环（≤8 轮）+ 流式输出。只读咨询角色（A 层），5 工具全部只读白名单硬编码。
//
// 设计 SSOT：docs/前端分析员设计.md。质量契约 §1：无引用不回答/数字只来自 SQL/
// 每问再接地/查不到说无数据/开放发明只出假设+验证路径/只读咨询非交易指令。
package analyst

import "context"

// Citation 工具执行产生的引用（流式回传前端，证据块）。
type Citation struct {
	Kind    string // sql / kb / decision / code / entity
	Ref     string // SQL 摘要 / 条目 ID / D# / path:line / entity type:id
	Excerpt string // 原文摘录（≤500 字）
}

// ToolCall 模型发起的工具调用（SSE content_block_start tool_use 解析）。
type ToolCall struct {
	ID    string
	Name  string
	Input string // raw JSON input（工具自行解析）
}

// ToolResult 工具执行结果（回填 messages API tool_result block）。
type ToolResult struct {
	ToolUseID string
	Content   string // 工具输出（JSON 字符串或文本）
	IsError   bool
}

// ToolExecutor 工具执行接口（loop 调用，tools.go 实现）。
// 执行产生 Citation 时经返回值流出（service 层转 proto 流给前端）。
type ToolExecutor interface {
	Execute(ctx context.Context, call ToolCall) (ToolResult, []Citation, error)
	// Names 返回工具名集（用于校验模型只调白名单工具）。
	Names() []string
}
