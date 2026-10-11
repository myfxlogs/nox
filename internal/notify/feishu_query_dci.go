// feishu_query_dci.go —— D-191②：执行口令「go」——业主一句触发 DCI covered-call
// 申购（LLM 前拦截紧随探针口令；幂等=executor 内 request_id 防重）。
package notify

import (
	"context"
	"strings"
)

// DCIGoRunner D-191：DCI 申购执行回调（cmd/core 装配 dciexec.Executor）。
type DCIGoRunner interface {
	Go(ctx context.Context) (string, error)
}

// SetDCIGoRunner 装配「go」口令后端（nil=未接线→回执「未接线」）。
func (h *FeishuQueryHandler) SetDCIGoRunner(r DCIGoRunner) { h.dciGo = r }

// isGoCmd 口令形：独立「go」一词（大小写不敏；防误伤「go ahead」类句子）。
func isGoCmd(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "go")
}

// handleGoCmd 执行 DCI 申购（回执=executor 原文；条件缺口以 err 文回显）。
func (h *FeishuQueryHandler) handleGoCmd(ctx context.Context) (string, error) {
	if h.dciGo == nil {
		return "DCI 申购执行面未接线（dciGo 未装配）", nil
	}
	msg, err := h.dciGo.Go(ctx)
	if err != nil {
		return "申购未执行：" + err.Error(), nil
	}
	return msg, nil
}
