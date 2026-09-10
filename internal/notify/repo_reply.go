// D-270 飞书回复解析+确认处理（S3）。
// 解析 OK/SKIP 指令，DB 层原子幂等（UPDATE WHERE feishu_event_id IS NULL），
// operated → 写 cnfi_actions（layer=liquidity, type=buy, rate_pct），SendText 发确认回执。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RepoReplyStore 回复处理存储接口（pgstore 实现，避免 notify→pgstore 依赖）。
type RepoReplyStore interface {
	FeishuTarget(ctx context.Context) (chatID, openID string, err error)
	GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error)
	ConfirmRepoOpOperated(ctx context.Context, id int64, amountCny, ratePct float64, operatedDate time.Time, eventID, messageID, via string) (bool, error)
	ConfirmRepoOpSkipped(ctx context.Context, id int64, reason, eventID, messageID, via string) (bool, error)
	AddCNFIAction(ctx context.Context, code, layer, actionType string, qty, amountCny float64, navAtAction, ratePct *float64, signalSnapshot, rationale, actionDate string) (int64, error)
}

// RepoReplyHandler 处理飞书回复指令（D-270）。
type RepoReplyHandler struct {
	store RepoReplyStore
	nowFn func() time.Time
}

// NewRepoReplyHandler 创建回复处理器。
func NewRepoReplyHandler(store RepoReplyStore) *RepoReplyHandler {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return &RepoReplyHandler{
		store: store,
		nowFn: func() time.Time { return time.Now().In(loc) },
	}
}

// SetNowFn 替换当前时间函数（测试注入）。
func (h *RepoReplyHandler) SetNowFn(fn func() time.Time) { h.nowFn = fn }

// instrumentCodes 品种名→code 映射。
var instrumentCodes = map[string]string{
	"GC001": "204001",
	"GC007": "204007",
	"GC014": "204014",
	"GC028": "204028",
}

// HandleReply 解析飞书回复指令，返回回复文本。
func (h *RepoReplyHandler) HandleReply(ctx context.Context, ev ReplyEvent) (string, error) {
	// 1. chat 校验
	boundChatID, boundOpenID, err := h.store.FeishuTarget(ctx)
	if err != nil {
		return "系统错误：无法读取推送目标", fmt.Errorf("feishu target: %w", err)
	}
	if boundChatID == "" {
		return "推送目标未绑定", nil
	}
	if ev.ChatID != boundChatID {
		return "非授权 chat", nil
	}
	// 2. 发送者校验
	if ev.SenderOpenID != boundOpenID {
		return "非授权发送者", nil
	}
	// 3. 消息类型校验
	if ev.MessageType != "text" {
		return "仅支持文本指令", nil
	}
	// 4. 解析 content JSON → 提取 text
	text, err := extractText(ev.Content)
	if err != nil {
		return "无法解析消息内容", nil
	}
	// 5. 解析指令
	text = strings.TrimSpace(text)
	upper := strings.ToUpper(text)
	switch {
	case strings.HasPrefix(upper, "OK "):
		return h.handleOK(ctx, ev, text)
	case strings.HasPrefix(upper, "SKIP"):
		return h.handleSKIP(ctx, ev, text)
	default:
		return "[CNY逆回购] 无法识别回复\n格式：\n  操作: OK GC001 50000 2.85\n  跳过: SKIP 原因", nil
	}
}

// extractText 从飞书 content JSON 提取 text 字段。
func extractText(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("empty content")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return "", err
	}
	return m["text"], nil
}

// handleOK 处理 OK 指令：OK <品种> <金额> <利率>。
func (h *RepoReplyHandler) handleOK(ctx context.Context, ev ReplyEvent, text string) (string, error) {
	parts := strings.Fields(text)
	if len(parts) < 4 {
		return "[CNY逆回购] 无法识别回复\n格式：\n  操作: OK GC001 50000 2.85\n  跳过: SKIP 原因", nil
	}
	name := strings.ToUpper(parts[1])
	code, ok := instrumentCodes[name]
	if !ok {
		return fmt.Sprintf("未知品种 %s（支持 GC001/GC007/GC014/GC028）", parts[1]), nil
	}
	var amount, rate float64
	if _, err := fmt.Sscanf(parts[2], "%f", &amount); err != nil {
		return "金额格式错误", nil
	}
	if _, err := fmt.Sscanf(parts[3], "%f", &rate); err != nil {
		return "利率格式错误", nil
	}
	if amount <= 0 {
		return "金额和利率必须为正数", nil
	}
	if rate <= 0 {
		return "金额和利率必须为正数", nil
	}
	// 找本周 pending opportunity
	now := h.nowFn()
	week := isoWeek(now)
	pending, err := h.store.GetPendingRepoOp(ctx, week)
	if err != nil {
		return "系统错误：查询待确认操作失败", fmt.Errorf("get pending: %w", err)
	}
	if pending == nil {
		return "本周无待确认操作", nil
	}
	// ConfirmRepoOpOperated（DB 层原子幂等）
	operatedDate := now
	ok2, err := h.store.ConfirmRepoOpOperated(ctx, pending.ID, amount, rate, operatedDate, ev.EventID, ev.MessageID, "feishu")
	if err != nil {
		return "系统错误：确认操作失败", fmt.Errorf("confirm operated: %w", err)
	}
	if !ok2 {
		return "已处理", nil // 幂等：已被另一并发处理
	}
	// 写 cnfi_actions（layer=liquidity, type=buy, qty=0, rate_pct）
	rationale := fmt.Sprintf("逆回购操作 %s ¥%.0f @%.2f%%（飞书确认）", name, amount, rate)
	if _, err := h.store.AddCNFIAction(ctx, code, "liquidity", "buy", 0, amount, nil, &rate, "{}", rationale, operatedDate.Format("2006-01-02")); err != nil {
		return "系统错误：写入投资笔记失败", fmt.Errorf("add cnfi action: %w", err)
	}
	return fmt.Sprintf("[CNY逆回购] 已记录 ✓\n%s ¥%.0f @%.2f%%  操作日 %s\n预计收益 ¥%.2f 已入投资笔记\n本周提醒已停止",
		name, amount, rate, operatedDate.Format("2006-01-02"),
		rate*amount*3/365), nil
}

// handleSKIP 处理 SKIP 指令：SKIP <原因>。
func (h *RepoReplyHandler) handleSKIP(ctx context.Context, ev ReplyEvent, text string) (string, error) {
	// SKIP 后的全部文本=原因
	idx := strings.Index(strings.ToUpper(text), "SKIP")
	reason := strings.TrimSpace(text[idx+4:])
	if reason == "" {
		return "SKIP 需提供原因", nil
	}
	now := h.nowFn()
	week := isoWeek(now)
	pending, err := h.store.GetPendingRepoOp(ctx, week)
	if err != nil {
		return "系统错误：查询待确认操作失败", fmt.Errorf("get pending: %w", err)
	}
	if pending == nil {
		return "本周无待确认操作", nil
	}
	ok2, err := h.store.ConfirmRepoOpSkipped(ctx, pending.ID, reason, ev.EventID, ev.MessageID, "feishu")
	if err != nil {
		return "系统错误：确认跳过失败", fmt.Errorf("confirm skipped: %w", err)
	}
	if !ok2 {
		return "已处理", nil
	}
	return fmt.Sprintf("[CNY逆回购] 已记录跳过 ✓\n原因：%s\n本周提醒已停止", reason), nil
}
