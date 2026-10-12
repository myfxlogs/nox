package analyst

// tools_probe.go D-100 llm_probe：受控判别实验探针（参数白名单，红线禁通用执行）。

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// ProbeCaller llm_probe 窄口：dashboard 装配闭包——当问解析 AI 配置裸调
// StreamMessages（无 loop 无工具=零递归）。返回 elapsedMs/chars/stop/errClass/err。
type ProbeCaller func(ctx context.Context, prompt string, maxTokens int, thinkingDisabled bool) (elapsedMs int, chars int, stop, errClass string, err error)

const (
	probeMaxPromptChars = 500  // 探针 prompt 上限（rune 计）
	probeMinTokens      = 256  // max_tokens 下限
	probeMaxTokens      = 8192 // max_tokens 上限
	probePerHour        = 10   // 每会话每小时限频（Tools 进程级窗近似）
)

// execLLMProbe 受控判别实验探针：参数白名单校验→限频→ProbeCaller 窄口，
// 返回 elapsed=Xs chars=N stop=<reason> err=<class|none> 结构化文本。
// 红线：禁通用代码执行——只允许 prompt/max_tokens/thinking_disabled 三参。
func (t *Tools) execLLMProbe(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		Prompt           string `json:"prompt"`
		MaxTokens        int    `json:"max_tokens"`
		ThinkingDisabled bool   `json:"thinking_disabled"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	if t.ProbeCaller == nil {
		return ToolResult{ToolUseID: call.ID, Content: "llm_probe 未配置（ProbeCaller 未接线）", IsError: true}, nil, nil
	}
	if args.Prompt == "" || utf8.RuneCountInString(args.Prompt) > probeMaxPromptChars {
		return ToolResult{ToolUseID: call.ID, Content: fmt.Sprintf("拒绝：prompt 空或超 %d 字符", probeMaxPromptChars), IsError: true}, nil, nil
	}
	if args.MaxTokens < probeMinTokens || args.MaxTokens > probeMaxTokens {
		return ToolResult{ToolUseID: call.ID, Content: fmt.Sprintf("拒绝：max_tokens 须 ∈ [%d,%d]", probeMinTokens, probeMaxTokens), IsError: true}, nil, nil
	}
	t.probeMu.Lock()
	if time.Since(t.probeWinAt) > time.Hour {
		t.probeWinAt = time.Now()
		t.probeWinCnt = 0
	}
	t.probeWinCnt++
	cnt := t.probeWinCnt
	t.probeMu.Unlock()
	if cnt > probePerHour {
		return ToolResult{ToolUseID: call.ID, Content: "拒绝：probe 限额（每会话每小时 ≤10 次）", IsError: true}, nil, nil
	}
	ms, chars, stop, errClass, err := t.ProbeCaller(ctx, args.Prompt, args.MaxTokens, args.ThinkingDisabled)
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: fmt.Sprintf("elapsed=%.1fs chars=%d stop=%s err=%s", float64(ms)/1000, chars, stop, errClass), IsError: true}, nil, nil
	}
	content := fmt.Sprintf("elapsed=%.1fs chars=%d stop=%s err=%s", float64(ms)/1000, chars, stop, errClass)
	cite := Citation{Kind: "code", Ref: "llm_probe", Excerpt: content}
	return ToolResult{ToolUseID: call.ID, Content: content}, []Citation{cite}, nil
}
