// D-284 S4：飞书回复路由分发。
// OK/SKIP→RepoReplyHandler（现有 CNY 逆回购指令），其余→FeishuQueryHandler（自然语言查询）。
package notify

import (
	"context"
	"strings"
)

// ReplyRouter 路由飞书回复：OK/SKIP→RepoReplyHandler，其余→FeishuQueryHandler。
type ReplyRouter struct {
	repoHandler  ReplyHandler
	queryHandler ReplyHandler
}

// NewReplyRouter 创建路由器。
func NewReplyRouter(repo, query ReplyHandler) *ReplyRouter {
	return &ReplyRouter{repoHandler: repo, queryHandler: query}
}

// HandleReply 按文本前缀路由：OK /SKIP→repoHandler，其余→queryHandler。
func (r *ReplyRouter) HandleReply(ctx context.Context, ev ReplyEvent) (string, error) {
	if ev.MessageType != "text" {
		return r.queryHandler.HandleReply(ctx, ev)
	}
	text, err := extractText(ev.Content)
	if err != nil {
		return r.queryHandler.HandleReply(ctx, ev)
	}
	upper := strings.ToUpper(strings.TrimSpace(text))
	if strings.HasPrefix(upper, "OK ") || strings.HasPrefix(upper, "SKIP") {
		return r.repoHandler.HandleReply(ctx, ev)
	}
	return r.queryHandler.HandleReply(ctx, ev)
}
