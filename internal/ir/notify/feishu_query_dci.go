// feishu_query_dci.go —— D-191②：执行口令「go」——业主一句触发 DCI covered-call
// 申购（LLM 前拦截紧随探针口令；幂等=executor 内 request_id 防重）。
package notify

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/infra/dephealth"
)

// DCIGoRunner D-191/D-189：DCI 申购执行回调（cmd/core 装配 dciexec.Executor；
// 提案绑定三态口令——go=最新 pending、go N=指定、不go N=declined）。
type DCIGoRunner interface {
	Go(ctx context.Context) (string, error)
	GoProposal(ctx context.Context, id int64) (string, error)
	DeclineProposal(ctx context.Context, id int64) (string, error)
}

// SetDCIGoRunner 装配「go」口令后端（nil=未接线→回执「未接线」）。
func (h *FeishuQueryHandler) SetDCIGoRunner(r DCIGoRunner) { h.dciGo = r }

// parseGoCmd 口令形（D-189③三态+D-195 拒批正名）：
//
//	「go」=裸词最新 pending；「go N」=指定提案；
//	「不go N」/「拒 N」/「驳回 N」=declined（不go 兼容保留；拒批别名同径）。
//
// 「拒绝」系与 #形不收——防吞探针域（probe 批准/拒绝#N 在更前解析）。
// 返回 id=0 表裸 go；ok=false 表非口令。
func parseGoCmd(text string) (id int64, decline bool, ok bool) {
	t := strings.TrimSpace(text)
	if strings.EqualFold(t, "go") {
		return 0, false, true
	}
	rest := t
	var numPart string
	switch {
	case strings.HasPrefix(rest, "不"):
		decline = true
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "不"))
		if len(rest) <= 2 || !strings.EqualFold(rest[:2], "go") {
			return 0, false, false
		}
		numPart = strings.TrimSpace(rest[2:])
	case strings.HasPrefix(rest, "驳回"):
		decline, numPart = true, strings.TrimSpace(rest[len("驳回"):])
	case strings.HasPrefix(rest, "拒") && !strings.HasPrefix(rest, "拒绝"):
		decline, numPart = true, strings.TrimSpace(rest[len("拒"):])
	default:
		if len(rest) <= 2 || !strings.EqualFold(rest[:2], "go") {
			return 0, false, false
		}
		numPart = strings.TrimSpace(rest[2:])
	}
	var n int64
	if _, err := fmt.Sscanf(numPart, "%d", &n); err != nil || n <= 0 {
		return 0, false, false
	}
	return n, decline, true
}

// handleGoCmd 执行/拒批 DCI 提案（回执=executor 原文；缺口以 err 文回显）。
func (h *FeishuQueryHandler) handleGoCmd(ctx context.Context, id int64, decline bool) (string, error) {
	dephealth.ContactHit("command") // 接触面犬哨：go 命令径命中
	if h.dciGo == nil {
		return "DCI 申购执行面未接线（dciGo 未装配）", nil
	}
	var msg string
	var err error
	switch {
	case decline:
		msg, err = h.dciGo.DeclineProposal(ctx, id)
	case id > 0:
		msg, err = h.dciGo.GoProposal(ctx, id)
	default:
		msg, err = h.dciGo.Go(ctx)
	}
	if err != nil {
		return "申购未执行：" + err.Error(), nil
	}
	return msg, nil
}
