// dci_check.go —— D-262 M4.3②：DCI 提案执行前检面归单包（命名闸）。
// dciexec.GoProposal 内联检查链抽出——腿异构不扭 TradeIntent（DCI 腿=venue 产品申购
// 非 venue 订单对），检面收 risk/gate 单包统一演进。
//
// 覆盖检面：提案态机/评估卡（M4.1③）/执行轨模板/休眠轨。
// 不覆盖（executor 域非分类检面）：执行租约/凭据/约束行读/实时报价复验。
package gate

import "fmt"

// DCIProposalView 提案检面投影（dciexec 读库后传值——本函数零 IO 纯判定）。
type DCIProposalView struct {
	ID         int64
	TemplateID string
	Venue      string
	Status     string // fin_proposal.status
	HasCard    bool   // fin_opp_eval 行存否
	Verdict    string // 评估卡 verdict（HasCard=false 时忽略）
	Dormant    bool   // dormant:<tmpl>@<venue> 命中否
}

// CheckDCI 命名闸：返回拒单原因串，""=过闸（后续执行径 venue 实检继续）。
func CheckDCI(p DCIProposalView) string {
	if p.Status != "proposed" {
		return fmt.Sprintf("提案#%d 已 %s（拒单）", p.ID, p.Status)
	}
	if !p.HasCard {
		return fmt.Sprintf("提案#%d 无评估卡（拒单——无卡不算候选）", p.ID)
	}
	if p.Verdict != "viable" {
		return fmt.Sprintf("提案#%d 评估卡=%s（拒单——非可执行候选）", p.ID, p.Verdict)
	}
	if _, ok := ExecTemplates[p.TemplateID]; !ok {
		return fmt.Sprintf("提案#%d 模板 %s 未接执行轨（提案可批执行面未接线）", p.ID, p.TemplateID)
	}
	if p.Dormant {
		return fmt.Sprintf("休眠轨（D-191R7）%s@%s（拒单）", p.TemplateID, p.Venue)
	}
	return ""
}
