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
}

// IsExecTemplate 提案可执行族判定（DCI 腿族 ∪ earn 族）——CheckDCI/autoexec/Go
// 三消费面同源。
func IsExecTemplate(tid string) bool {
	if _, ok := ExecTemplates[tid]; ok {
		return true
	}
	return EarnTemplates[tid]
}

// ExecTemplateVenues 可执行提案模板→执行 venue（D-193① venue 悬空自检数据源；
// 本 executor=binance 单所写径——venue 集从腿面衍生非手抄）。
func ExecTemplateVenues() map[string]string {
	out := make(map[string]string, len(ExecTemplates)+len(EarnTemplates))
	for t := range ExecTemplates {
		out[t] = "binance"
	}
	for t := range EarnTemplates {
		out[t] = "binance" // earn 写径当前仅 binance（okx earn 未接）
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
