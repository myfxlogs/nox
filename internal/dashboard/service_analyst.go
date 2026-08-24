package dashboard

// service_analyst.go D-091 前端分析员 RPC：AskAnalyst（server streaming）+ AppendAnalystNote。
// 装配经 SetAnalyst 注入（main 构造 analyst deps 后注入，与 SetSettler 同模式）。
// 未注入（cfg.AIAPIKey 空）→ AskAnalyst 返回 failed_precondition，AppendAnalystNote 返回 unavailable。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arb/internal/analyst"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// analystDeps 分析员依赖（main 注入；nil = 未配置）。
type analystDeps struct {
	client   *analyst.Client
	tools    *analyst.Tools
	rateLim  *analyst.RateLimiter
	knRoot   string // 经验库根（默认 /kn）
	notePath string // 待归纳文件相对 knRoot 路径
}

// SetAnalyst 注入分析员依赖（main 装配后调用）。
func (s *Service) SetAnalyst(client *analyst.Client, tools *analyst.Tools, rateLim *analyst.RateLimiter, knRoot, notePath string) {
	s.analyst = &analystDeps{client: client, tools: tools, rateLim: rateLim, knRoot: knRoot, notePath: notePath}
}

// AskAnalyst 流式回答：限流 → 构造 messages（系统提示词 + 上下文 + 历史 + 问题）→ analyst.Run → delta/citation/follow_ups/done 流出。
func (s *Service) AskAnalyst(ctx context.Context, req *connect.Request[v1.AskAnalystRequest], stream *connect.ServerStream[v1.AskAnalystResponse]) error {
	deps := s.analyst
	if deps == nil || deps.client == nil || deps.client.APIKey == "" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("分析员未配置（ARB_AI_API_KEY 为空）"))
	}
	if deps.rateLim != nil && !deps.rateLim.Allow() {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("已达每小时提问上限"))
	}
	// 构造初始 messages：历史轮次 + 本轮问题（含上下文摘要注入）。
	msgs := buildAnalystMessages(req.Msg)
	// 循环回调 → proto 流。
	cb := analyst.LoopCallbacks{
		OnDelta: func(text string) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Delta{Delta: text}})
		},
		OnCitation: func(c analyst.Citation) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Citation{Citation: &v1.AnalystCitation{
				Kind: c.Kind, Ref: c.Ref, Excerpt: c.Excerpt,
			}}})
		},
		OnFollowUps: func(fu []string) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_FollowUps{FollowUps: &v1.SuggestedFollowUps{FollowUps: fu}}})
		},
	}
	result, err := analyst.Run(ctx, deps.client, deps.tools, msgs, cb)
	if err != nil {
		return fmt.Errorf("analyst run: %w", err)
	}
	// 末帧 done（grounded=false → 前端灰显「⚠ 无数据支撑」）。
	return stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Done{Done: &v1.AnalystDone{Grounded: result.Grounded}}})
}

// AppendAnalystNote 追加「记一下」到 docs/经验库/待归纳-分析员.md（并发安全 append）。
func (s *Service) AppendAnalystNote(ctx context.Context, req *connect.Request[v1.AppendAnalystNoteRequest]) (*connect.Response[v1.AppendAnalystNoteResponse], error) {
	deps := s.analyst
	if deps == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("分析员未配置"))
	}
	if req.Msg.Question == "" || req.Msg.Summary == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("question/summary 不能为空"))
	}
	root := deps.knRoot
	if root == "" {
		root = analyst.KnRoot
	}
	full := filepath.Join(root, deps.notePath)
	// 路径仍在 root 下（防篡改）。
	if rel, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rel, "..") {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("笔记路径非法"))
	}
	var cites []string
	for _, c := range req.Msg.Citations {
		cites = append(cites, fmt.Sprintf("  - [%s] %s: %s", c.Kind, c.Ref, c.Excerpt))
	}
	block := fmt.Sprintf("\n## %s\n- 日期: %s\n- 问题: %s\n- 摘要: %s\n- 引用:\n%s\n",
		"分析员笔记", time.Now().Format("2006-01-02 15:04"), req.Msg.Question, req.Msg.Summary, strings.Join(cites, "\n"))
	if err := appendNote(full, block); err != nil {
		return nil, fmt.Errorf("append note: %w", err)
	}
	return connect.NewResponse(&v1.AppendAnalystNoteResponse{Ok: true}), nil
}

// appendNote 并发安全追加（文件锁互斥，防多请求交错）。
var noteMu sync.Mutex

func appendNote(path, block string) error {
	noteMu.Lock()
	defer noteMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}

// buildAnalystMessages 构造初始 messages：历史轮次 + 本轮问题（含上下文摘要）。
func buildAnalystMessages(req *v1.AskAnalystRequest) []analyst.Message {
	var msgs []analyst.Message
	for _, turn := range req.History {
		role := "assistant"
		if turn.User {
			role = "user"
		}
		msgs = append(msgs, analyst.TextContent(role, turn.Text))
	}
	// 本轮问题：注入页面 + 上下文实体引用摘要。
	parts := []string{req.Question}
	if req.Page != "" {
		parts = append([]string{fmt.Sprintf("[当前页面: %s]", req.Page)}, parts...)
	}
	if len(req.Context) > 0 {
		var refs []string
		for _, e := range req.Context {
			refs = append(refs, fmt.Sprintf("%s:%s", e.Type, e.Id))
		}
		parts = append([]string{fmt.Sprintf("[上下文实体: %s]", strings.Join(refs, ", "))}, parts...)
	}
	msgs = append(msgs, analyst.TextContent("user", strings.Join(parts, "\n")))
	return msgs
}
