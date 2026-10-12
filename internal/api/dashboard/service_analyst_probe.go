package dashboard

// service_analyst_probe.go D-100 llm_probe 装配：当问解析 AI 配置的受控探针闭包。

import (
	"context"
	"strings"
	"time"

	"nox/internal/platform/analyst"
)

// ProbeCallerForAnalyst 返回当问解析 AI 配置的探针闭包——每调 resolveAiConfig
// （DB 热读同 AskAnalyst）构造 client 裸调 StreamMessages（无 loop 无 tools=零递归；
// D-087 中性化经 client 自动应用）。
// thinkingDisabled 参数保留（MessageRequest.Thinking 字段=D-098 域，落地后接线）。
func (s *Service) ProbeCallerForAnalyst() analyst.ProbeCaller {
	return func(ctx context.Context, prompt string, maxTokens int, _ bool) (int, int, string, string, error) {
		client, err := s.resolveAiConfig(ctx)
		if err != nil {
			return 0, 0, "", "config", err
		}
		start := time.Now()
		chars := 0
		req := &analyst.MessageRequest{
			Model:     client.Model,
			MaxTokens: maxTokens,
			Messages:  []analyst.Message{analyst.TextContent("user", prompt)},
			Stream:    true,
		}
		stop, err := client.StreamMessages(ctx, req, analyst.StreamCallbacks{
			OnDelta: func(t string) { chars += len(t) },
		})
		ms := int(time.Since(start).Milliseconds())
		if err != nil {
			return ms, chars, stop, probeErrClass(err), err
		}
		return ms, chars, stop, "none", nil
	}
}

// probeErrClass 探针错误分层标签（运输/超时/http/其它——判别实验归因用）。
func probeErrClass(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "deadline") || strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.HasPrefix(msg, "API "):
		return "http"
	default:
		return "transport"
	}
}
