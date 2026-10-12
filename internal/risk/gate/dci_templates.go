package gate

// ExecTemplates 可执行提案模板→腿映射（D-189：提案模板=腿子集；
// 新模板增 legs 在此登记，venue 悬空自检与执行入口同源消费——M2.4 自 dciexec 移入，
// oppmatch 读径 require research↛trading 红线，模板组成属闸域件）。
var ExecTemplates = map[string][]DCILeg{
	"dci_sell_high_covered_call": {CallLeg},
	"dci_buy_low":                {PutLeg},
}

// ExecTemplateVenues 可执行提案模板→执行 venue（D-193① venue 悬空自检数据源；
// 本 executor=binance 单所写径——venue 集从腿面衍生非手抄）。
func ExecTemplateVenues() map[string]string {
	out := make(map[string]string, len(ExecTemplates))
	for t := range ExecTemplates {
		out[t] = "binance"
	}
	return out
}
