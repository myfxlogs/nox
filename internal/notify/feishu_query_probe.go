// feishu_query_probe.go —— D-176②：探针审批口令「批准#N [D-xxx]」「拒绝#N [理由]」。
// 与前端按钮同路由（ApproveProbe/RejectProbe 等价物），幂等应答=RPC 原态回显。
package notify

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// probeCmdRe 口令形：批准#20 / 批准 #20 D-176 / 拒绝#20 理由……
var probeCmdRe = regexp.MustCompile(`^(批准|拒绝)\s*#\s*(\d+)\s*(.*)$`)

// probeDecisionRefRe 与 ApproveProbe RPC 同一校验（^D-\d{3,}$）。
var probeDecisionRefRe = regexp.MustCompile(`^D-\d{3,}$`)

type probeCmd struct {
	verb string // approve / reject
	id   int64
	arg  string
}

func parseProbeCmd(text string) (probeCmd, bool) {
	m := probeCmdRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return probeCmd{}, false
	}
	var id int64
	fmt.Sscanf(m[2], "%d", &id)
	verb := "approve"
	if m[1] == "拒绝" {
		verb = "reject"
	}
	return probeCmd{verb: verb, id: id, arg: strings.TrimSpace(m[3])}, id > 0
}

// handleProbeCmd 口令执行（同步回执，webhook 发 SendText——同 handleAck 形态）。
func (h *FeishuQueryHandler) handleProbeCmd(ctx context.Context, cmd probeCmd) (string, error) {
	if h.probeDecide == nil {
		return "探针审批面未接线（probeDecider 未装配）", nil
	}
	switch cmd.verb {
	case "approve":
		if !probeDecisionRefRe.MatchString(cmd.arg) {
			return fmt.Sprintf("批准需附决策引用：批准#%d D-xxx（如 批准#%d D-176）", cmd.id, cmd.id), nil
		}
		status, msg, posID, err := h.probeDecide.ApproveProbe(ctx, cmd.id, cmd.arg)
		if err != nil {
			return fmt.Sprintf("提案#%d 批准失败：%v", cmd.id, err), nil
		}
		if posID > 0 {
			return fmt.Sprintf("提案#%d 已批准开仓 pos=%d（%s）", cmd.id, posID, status), nil
		}
		if msg != "" {
			return fmt.Sprintf("提案#%d → %s（%s）", cmd.id, status, msg), nil
		}
		return fmt.Sprintf("提案#%d → %s", cmd.id, status), nil
	case "reject":
		reason := cmd.arg
		if reason == "" {
			reason = "飞书口令拒绝"
		}
		status, msg, err := h.probeDecide.RejectProbe(ctx, cmd.id, reason)
		if err != nil {
			return fmt.Sprintf("提案#%d 拒绝失败：%v", cmd.id, err), nil
		}
		if msg != "" {
			return fmt.Sprintf("提案#%d → %s（%s）", cmd.id, status, msg), nil
		}
		return fmt.Sprintf("提案#%d → %s", cmd.id, status), nil
	}
	return "", nil
}
