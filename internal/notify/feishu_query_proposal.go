// feishu_query_proposal.go —— D-194：「提案」整词口令→pending 提案列表回执。
// 只读不写、仅 proposed 态、零新定时器（口令即查询非业务数据刷新）。
package notify

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"arb/internal/store/pgstore"
)

// ProposalLister D-194：提案清单回调（cmd/core 装配 *pgstore.Store，同 DCIGoRunner 模式）。
type ProposalLister interface {
	ListProposals(ctx context.Context, status string, limit int) ([]pgstore.Proposal, error)
}

// SetProposalLister 装配「提案」口令后端（nil=未接线→回执「未接线」）。
func (h *FeishuQueryHandler) SetProposalLister(l ProposalLister) { h.propLister = l }

// isProposalCmd 口令形：独立「提案」整词（trim 全等——长句含「提案」落 LLM，禁宽匹配）。
func isProposalCmd(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "提案")
}

// handleProposalCmd pending 提案逐行回执（proposed 态、id 序、限 10）：
// #id|模板|venue|params键值压缩（异构 schema 按行实际键取值禁固定假设）|apr；
// 空集如实报无；尾附「go N 批准·拒 N 驳回」提示（不go N 兼容保留）。
func (h *FeishuQueryHandler) handleProposalCmd(ctx context.Context) (string, error) {
	if h.propLister == nil {
		return "提案清单面未接线（propLister 未装配）", nil
	}
	rows, err := h.propLister.ListProposals(ctx, "proposed", 10)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "当前无 pending 提案（oppmatch 扫描产出后此列可见）", nil
	}
	var b strings.Builder
	b.WriteString("待批提案（proposed，按 id 序限 10）：\n")
	for _, p := range rows {
		apr := "—"
		if p.ExpectedAPR != nil {
			apr = fmt.Sprintf("apy=%.4g", *p.ExpectedAPR)
		}
		fmt.Fprintf(&b, "#%d %s|%s|%s|%s\n", p.ID, p.TemplateID, p.Venue, paramsDigest(p.Params), apr)
	}
	b.WriteString("——回「go N」批准提案 · 「拒 N」驳回")
	return strings.TrimRight(b.String(), "\n"), nil
}

// paramsDigest 异构快照键值压缩（按行实际键序取，禁固定 schema 假设；值超 12 字截断）。
func paramsDigest(m map[string]any) string {
	if len(m) == 0 {
		return "无快照"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := fmt.Sprintf("%v", m[k])
		if len(v) > 12 {
			v = v[:11] + "…"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "·")
}
