// ④：飞书 ack 拦截面——独立文件收编防 feishu_query.go 超 300 行。
package notify

import (
	"context"
	"fmt"
)

// isAckWord D-168：裸词 ack 判定（仅整词匹配防误拦查询句）。
func isAckWord(text string) bool {
	switch text {
	case "收到", "知道了", "ack", "ACK", "Ack", "OK", "ok":
		return true
	}
	return false
}

// handleAck ack 指令执行（同步返回回执，webhook 发 SendText）。
func (h *FeishuQueryHandler) handleAck(ctx context.Context, ev ReplyEvent) (string, error) {
	if ev.ParentID != "" {
		key, ok, err := h.store.AckAlertEpisodeByPushMsg(ctx, ev.ParentID)
		if err != nil {
			return "系统错误：确认失败", fmt.Errorf("ack episode by push msg: %w", err)
		}
		if !ok {
			return "未找到对应告警集（或已确认）", nil
		}
		n, err := h.store.AckUnackedAlertsByKey(ctx, key)
		if err != nil {
			return "已确认该告警集（alerts 同步失败）", fmt.Errorf("ack alerts by key: %w", err)
		}
		return fmt.Sprintf("已确认该告警集（同步 %d 条告警）", n), nil
	}
	n, err := h.store.AckAllAlertEpisodes(ctx)
	if err != nil {
		return "系统错误：确认失败", fmt.Errorf("ack all episodes: %w", err)
	}
	if err := h.store.AckAll(ctx); err != nil {
		return "已确认 episode（alerts 同步失败）", fmt.Errorf("ack all alerts: %w", err)
	}
	return fmt.Sprintf("已确认 %d 条告警", n), nil
}
