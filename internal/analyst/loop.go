package analyst

// loop.go agent 循环 ≤8 轮工具调用；每轮 tool_use→执行→tool_result 回填；
// 最终文本增量经回调流出；全程零工具调用即 grounded=false 信号。
// 设计 §2：单答成本护栏 max_tokens + 工具循环 ≤8 轮。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxToolRounds 工具调用最大轮数（防失控长答）。
const MaxToolRounds = 8

// LoopCallbacks 循环回调（service 层注入，转 proto 流给前端）。
type LoopCallbacks struct {
	OnDelta     func(text string)
	OnCitation  func(c Citation)
	OnFollowUps func(followUps []string)
}

// LoopResult 循环结果。
type LoopResult struct {
	Grounded bool // 全程零工具调用=false
}

// Run 执行 agent 循环。
// 初始 messages 由调用方构造（含系统提示词注入的首条 user 消息）；
// 每轮：StreamMessages → delta 流出 + tool_use 收集 → stop_reason=tool_use 时执行工具回填 → 下一轮；
// stop_reason=end_turn 或 max_tokens 时结束；≤8 轮硬截断。
func Run(ctx context.Context, c *Client, tools ToolExecutor, msgs []Message, cb LoopCallbacks) (LoopResult, error) {
	toolSpecs := ToolSpecs()
	grounded := false
	for round := 0; round < MaxToolRounds; round++ {
		req := &MessageRequest{
			Model:     c.Model,
			MaxTokens: 2048,
			System:    SystemPrompt,
			Messages:  msgs,
			Stream:    true,
			Tools:     toolSpecs,
		}
		var toolCalls []ToolCall
		var assistantBlocks []ContentBlock
		stopReason, err := c.StreamMessages(ctx, req, StreamCallbacks{
			OnDelta: func(text string) {
				if cb.OnDelta != nil {
					cb.OnDelta(text)
				}
			},
			OnToolUse: func(call ToolCall) {
				toolCalls = append(toolCalls, call)
				assistantBlocks = append(assistantBlocks, ContentBlock{
					Type:  "tool_use",
					ID:    call.ID,
					Name:  call.Name,
					Input: json.RawMessage(call.Input),
				})
			},
		})
		if err != nil {
			return LoopResult{Grounded: grounded}, fmt.Errorf("stream round %d: %w", round, err)
		}
		// 收集 assistant 消息（含 text + tool_use blocks）
		if len(assistantBlocks) > 0 {
			msgs = append(msgs, BlocksContent("assistant", assistantBlocks))
		}
		if len(toolCalls) > 0 {
			grounded = true
		}
		if stopReason != "tool_use" || len(toolCalls) == 0 {
			// end_turn / max_tokens / stop_sequence → 结束
			break
		}
		// 执行工具 + 回填 tool_result
		var resultBlocks []ContentBlock
		for _, tc := range toolCalls {
			result, cites, err := tools.Execute(ctx, tc)
			if err != nil {
				result = ToolResult{ToolUseID: tc.ID, Content: "工具执行错误: " + err.Error(), IsError: true}
			}
			for _, cite := range cites {
				if cb.OnCitation != nil {
					cb.OnCitation(cite)
				}
			}
			contentJSON, _ := json.Marshal(result.Content)
			resultBlocks = append(resultBlocks, ContentBlock{
				Type:      "tool_result",
				ToolUseID: result.ToolUseID,
				Content:   contentJSON,
				IsError:   result.IsError,
			})
		}
		msgs = append(msgs, BlocksContent("user", resultBlocks))
	}
	// 末轮：尝试解析 follow_ups（模型可能在最后文本里输出 JSON）
	// 省略亦可——前端容忍空。此处简单解析末条 assistant 文本中的 "follow_ups": [...]
	if cb.OnFollowUps != nil {
		if fu := parseFollowUps(msgs); len(fu) > 0 {
			cb.OnFollowUps(fu)
		}
	}
	return LoopResult{Grounded: grounded}, nil
}

// parseFollowUps 从末条 assistant 文本中尝试解析 follow_ups（best-effort，非硬要求）。
func parseFollowUps(msgs []Message) []string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		var text string
		if err := json.Unmarshal(msgs[i].Content, &text); err != nil {
			// block 数组形式，跳过（末轮通常纯文本）
			continue
		}
		idx := strings.Index(text, "follow_ups")
		if idx < 0 {
			return nil
		}
		// 找最近的 JSON 数组
		start := strings.Index(text[idx:], "[")
		end := strings.LastIndex(text[idx:], "]")
		if start < 0 || end < 0 || end <= start {
			return nil
		}
		var fu []string
		if err := json.Unmarshal([]byte(text[idx+start:idx+end+1]), &fu); err == nil {
			return fu
		}
		return nil
	}
	return nil
}
