package gate

import "strings"

// ExecTemplates 可执行提案模板→腿映射（D-189：提案模板=腿子集；
// 新模板增 legs 在此登记，venue 悬空自检与执行入口同源消费——M2.4 自 dciexec 移入，
// oppmatch 读径 require research↛trading 红线，模板组成属闸域件）。
var ExecTemplates = map[string][]DCILeg{
	"dci_sell_high_covered_call": {CallLeg},
	"dci_buy_low":                {PutLeg},
}

// EarnTemplates earn 执行族（D-286 金钱不眠——cash_mgmt/earn_apr_lending 提案
// 可执行面）：venue 产品申购非腿集，不入 ExecTemplates 腿映射——可执行族判定
// 走 IsExecTemplate；执行径=dciexec.goEarn（venue 写径同 DCI=binance）。
var EarnTemplates = map[string]bool{
	"cash_mgmt":        true,
	"earn_apr_lending": true,
	"earn_rotation":    true, // D-612：息差轮动所内赎转（redeem→subscribe 同 venue）
}

// IsExecTemplate 提案可执行族判定（DCI 腿族 ∪ earn 族）——CheckDCI/autoexec/Go
// 三消费面同源。
func IsExecTemplate(tid string) bool {
	if _, ok := ExecTemplates[tid]; ok {
		return true
	}
	return EarnTemplates[tid]
}

// DCIExecVenues DCI 腿族执行轨覆盖 venue 集（GoProposal 分派面：
// binance=tryLeg、okx=tryLegOKX；新 venue 接径在此补名——oppmatch 提案
// 枚举与 venue 悬空自检同源消费）。
var DCIExecVenues = map[string]bool{"binance": true, "okx": true}

// EarnExecVenues earn 族执行轨覆盖 venue 集（dciexec.earnRail 分派面：
// binance/okx/gate 三径；新 venue 接径在此补名——同上双消费面同源）。
var EarnExecVenues = map[string]bool{"binance": true, "okx": true, "gate": true}

// ExecTemplateVenueSets 可执行提案模板→执行轨 venue 覆盖集（D-193① venue
// 悬空自检数据源）。语义=「模板声明凭据 venue ∈ rail 覆盖集」——earn/DCI
// rail 已多 venue 化，提案 venue 在覆盖集内由执行器按 venue 路由（旧单
// venue 映射把 okx:spot 凭据判悬空=漂移误报源）。
func ExecTemplateVenueSets() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(ExecTemplates)+len(EarnTemplates))
	for t := range ExecTemplates {
		out[t] = DCIExecVenues
	}
	for t := range EarnTemplates {
		out[t] = EarnExecVenues
	}
	return out
}

// StrategySettleCcy D-522③：策略结算币种读口——derive 键取模板注册面不硬编。
// ExecTemplates→腿 InvestCcy（dci_buy_low→USDT、sell_high→BTC）；EarnTemplates→USDT
// （earn 申购定值域）；MT/CFD 轨=USD 保证金结算户。未知策略→false（预算面 fail-closed）。
func StrategySettleCcy(strategy string) (string, bool) {
	low := strings.ToLower(strategy)
	if legs, ok := ExecTemplates[low]; ok && len(legs) > 0 {
		return legs[0].InvestCcy, true
	}
	if EarnTemplates[low] {
		return "USDT", true
	}
	switch strings.ToUpper(strategy) {
	case "XASSET_CARRY", "SWAPARB", "CARRY":
		return "USD", true
	}
	return "", false
}
