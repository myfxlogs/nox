// resume.go —— D-198：提案断点续执（首个 cexscan DCI 采集拍后一次性）。
package dciexec

import (
	"context"
	"fmt"
)

// ResumeFailedProposals D-198 提案断点续执——proposed 且最新单 failed 终态的提案
// 各续执一次（pending/submitted 飞行行不入集；无单提案天然不触发）。
// 续执前 outstanding DCI 复验：同 product_ref+腿向已锁仓→executed 零重购（venue
// 实收本地记 failed 的错位兜住）；读败→跳过+runtime gap 不重发。续执走 GoProposal
// 同径全闸复跑（spot/余额/OTM/tenor/租约重验），成交/败因走 alert 推送。
func (e *Executor) ResumeFailedProposals(ctx context.Context) (int, error) {
	rows, err := e.Store.ListFailedProposalOrders(ctx)
	if err != nil {
		return 0, err
	}
	resumed := 0
	for _, r := range rows {
		// 返修①：venue 侧持仓读为主（venue 实收本地记 failed 的错位只此径能兜），
		// 本地 filled 检查降为补充；venue 读败→跳过+runtime gap 不重发。
		has, herr := e.venueDCIHolding(ctx, r.ProductRef, r.OptType)
		if herr != nil {
			_ = e.gap(ctx, callLeg, "runtime", "dual_quote",
				"系统侧病灶：提案#%d 续执前 venue 持仓读败（%v）——跳过不重发", r.ProposalID, herr)
			continue
		}
		if !has {
			var lerr error
			if has, lerr = e.Store.OutstandingDCIProduct(ctx, r.ProductRef, r.OptType); lerr != nil {
				_ = e.gap(ctx, callLeg, "runtime", "dual_quote",
					"系统侧病灶：提案#%d 续执前本地 outstanding 读败（%v）——跳过不重发", r.ProposalID, lerr)
				continue
			}
		}
		if has {
			if serr := e.Store.SetProposalStatus(ctx, r.ProposalID, "executed"); serr != nil {
				return resumed, serr
			}
			_ = e.Store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"提案#%d 断点续执：%s %s 已锁仓在期→executed 零重购", r.ProposalID, r.ProductRef, r.OptType))
			continue
		}
		msg, gerr := e.GoProposal(ctx, r.ProposalID)
		if gerr != nil {
			_ = e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
				"提案#%d 断点续执败：%v", r.ProposalID, gerr))
			continue
		}
		resumed++
		_ = e.Store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
			"提案#%d 断点续执：%s", r.ProposalID, msg))
	}
	return resumed, nil
}
