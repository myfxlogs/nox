// dormant.go —— D-204 休眠轨机器可读面（拆出防 exec.go 超 450 硬闸）：
// dormantPairs 约束集→"tmpl@venue" 键集；skipDormant 裸 go 次席扫；
// GoProposal 休眠闸在 exec.go 内联（拒单带因非市况文案）。
package dciexec

import (
	"context"

	"nox/internal/store/pgstore"
)

// dormantPairs D-204③：约束表休眠轨集（读败=err 上抛——执行闸面如实报）。
func (e *Executor) dormantPairs(ctx context.Context) (map[string]bool, error) {
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return nil, err
	}
	return pgstore.DormantPairs(cons), nil
}

// skipDormant 裸 go 候选校正：最新 pending 中休眠轨→proposed 集 id DESC 扫至
// 首个可执行且非休眠提案（tmpls=execTemplates 键集白名单，次席语义）。
func (e *Executor) skipDormant(ctx context.Context, p *pgstore.Proposal, tmpls []string) (*pgstore.Proposal, error) {
	dormant, err := e.dormantPairs(ctx)
	if err != nil {
		return p, nil // 约束读败放行——休眠是降速语义非风控硬闸（go 主径不扩闸）
	}
	if !dormant[p.TemplateID+"@"+p.Venue] {
		return p, nil
	}
	list, err := e.Store.ListProposals(ctx, "proposed", 50)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	for _, t := range tmpls {
		allow[t] = true
	}
	for i := range list {
		if !allow[list[i].TemplateID] || dormant[list[i].TemplateID+"@"+list[i].Venue] {
			continue
		}
		return &list[i], nil
	}
	return nil, nil
}
