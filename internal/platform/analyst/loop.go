package analyst

// loop.go agent 循环 ≤8 轮工具调用；每轮 tool_use→执行→tool_result 回填；
// 最终文本增量经回调流出；全程零工具调用即 grounded=false 信号。
// 设计 §2：单答成本护栏 max_tokens + 工具循环 ≤8 轮。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
	Grounded   bool   // 全程零工具调用=false
	StopReason string // D-530：末轮 stop_reason（end_turn/max_tokens/...，symdig 截断检测消费）
	// Usage D-244：全轮次 token 耗合计（message_start in / message_delta out 累积；
	// 报告类调用方留痕用，流式缺 usage 事件时为 0 不误报）。
	InputTokens  int
	OutputTokens int
}

// Run 执行 agent 循环（硬编码 ToolSpecs()+SystemPrompt）。
// 初始 messages 由调用方构造（含系统提示词注入的首条 user 消息）；
// 每轮：StreamMessages → delta 流出 + tool_use 收集 → stop_reason=tool_use 时执行工具回填 → 下一轮；
// stop_reason=end_turn 或 max_tokens 时结束；≤8 轮硬截断。
func Run(ctx context.Context, c *Client, tools ToolExecutor, msgs []Message, cb LoopCallbacks) (LoopResult, error) {
	return RunWithSpecs(ctx, c, tools, ToolSpecs(), SystemPrompt, msgs, cb)
}

// RunWithSpecs 执行 agent 循环（toolSpecs/systemPrompt 由调用方注入）。
// 飞书查询用专用工具+提示词，不复用前端分析员的 ToolSpecs/SystemPrompt。
// RunWithSpecs max_tokens 缺省 8192（D-064 后 glm-5.3 思考模型：thinking 块与 text 共享预算，
// 2048 下重任务（explorer 日扫四段+10 提案）thinking 吃满致 JSON 截断整批零落库——10-04 22:47
// 首秀实拍；8192 = D-530 symdig 先例值。max_tokens 是上限非预付，轻任务实际消耗不变）。
func RunWithSpecs(ctx context.Context, c *Client, tools ToolExecutor, toolSpecs []ToolSpec, systemPrompt string, msgs []Message, cb LoopCallbacks) (LoopResult, error) {
	return RunWithSpecsMaxTokens(ctx, c, tools, toolSpecs, systemPrompt, msgs, cb, 8192)
}

// RunWithSpecsMaxTokens 同 RunWithSpecs，maxTokens 显式可调（D-530 symdig 8192 缺省）。
func RunWithSpecsMaxTokens(ctx context.Context, c *Client, tools ToolExecutor, toolSpecs []ToolSpec, systemPrompt string, msgs []Message, cb LoopCallbacks, maxTokens int) (LoopResult, error) {
	return RunWithSpecsMaxTokensThinking(ctx, c, tools, toolSpecs, systemPrompt, msgs, cb, maxTokens, nil)
}

// RunWithSpecsMaxTokensThinking 同 RunWithSpecsMaxTokens，thinking 显式可控（D-098）——
// thinking 非 nil 原样透传请求体（explorer 三面传 `{"type":"disabled"}`：结构化 JSON 输出
// 面思考纯耗时限）；nil=不设字段（analyst 会话保留思考，请求体字节级与旧一致）。
func RunWithSpecsMaxTokensThinking(ctx context.Context, c *Client, tools ToolExecutor, toolSpecs []ToolSpec, systemPrompt string, msgs []Message, cb LoopCallbacks, maxTokens int, thinking json.RawMessage) (LoopResult, error) {
	grounded := false
	lastStop := ""
	inTok, outTok := 0, 0
	for round := 0; round < MaxToolRounds; round++ {
		req := &MessageRequest{
			Model:     c.Model,
			MaxTokens: maxTokens,
			System:    systemPrompt,
			Messages:  msgs,
			Stream:    true,
			Tools:     toolSpecs,
			Thinking:  thinking,
		}
		var toolCalls []ToolCall
		var assistantBlocks []ContentBlock
		rIn, rOut := 0, 0 // D-246：逐轮 usage（账本按 HTTP 调用粒度落行）
		rStart := time.Now()
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
			OnUsage: func(in, out int) {
				inTok += in
				outTok += out
				rIn += in
				rOut += out
			},
		})
		if c.CallLog != nil { // D-246：fin_llm_call 每次真实调用落行
			rec := CallRecord{Model: c.Model, InTokens: rIn, OutTokens: rOut,
				ElapsedMs: int(time.Since(rStart).Milliseconds())}
			if err != nil {
				rec.Err = err.Error()
			}
			c.CallLog(ctx, rec)
		}
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
		lastStop = stopReason
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
	return LoopResult{Grounded: grounded, StopReason: lastStop, InputTokens: inTok, OutputTokens: outTok}, nil
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
