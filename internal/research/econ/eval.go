// eval.go —— D-258 M4.1：机会评估卡纯算（v3 §7 验证器核心）。
// 净 APR = 毛 − 费 − 滑点 − 失败成本 − 市窗损耗（五项全 APR% 年化口径）。
// 纪律：逐项须有 venue/账本证据（Term.Evidence 非空）；任一无证据→
// unevaluable（fail-closed 不外推）；全有证据按净符号 viable|reject。
// 机理不适用项由装配侧给 Evidence="mechanism:..."（如 DCI 申购锁价滑点
// 不适用）——属确定性模板语义非外推。
package econ

// Term 单项拆解：Value=APR% 年化点；Evidence=证据出处（空=缺证据）。
type Term struct {
	Value    float64
	Evidence string
}

// EvalInput 评估输入（装配侧以 venue/账本查询喂入）。
type EvalInput struct {
	TemplateID string
	Gross      Term
	Fee        Term
	Slippage   Term
	Failure    Term
	Window     Term
}

// EvalCard 评估卡产出（Missing=缺证据项名集）。
type EvalCard struct {
	Net      float64
	Verdict  string // viable|unevaluable|reject
	Missing  []string
	Evidence map[string]string
	Terms    [5]float64 // gross/fee/slippage/failure/window（缺项=0，列名对齐 fin_opp_eval）
}

// Evaluate 五项拆解→卡。缺证据优先于净符号（unevaluable 一票否决）。
func Evaluate(in EvalInput) EvalCard {
	c := EvalCard{Evidence: map[string]string{}}
	terms := []struct {
		name string
		t    Term
	}{
		{"gross", in.Gross}, {"fee", in.Fee}, {"slippage", in.Slippage},
		{"failure", in.Failure}, {"window", in.Window},
	}
	for i, x := range terms {
		if x.t.Evidence == "" {
			c.Missing = append(c.Missing, x.name)
			continue
		}
		c.Terms[i] = x.t.Value
		c.Evidence[x.name] = x.t.Evidence
	}
	if len(c.Missing) > 0 {
		c.Verdict = "unevaluable"
		return c
	}
	c.Net = c.Terms[0] - c.Terms[1] - c.Terms[2] - c.Terms[3] - c.Terms[4]
	if c.Net > 0 {
		c.Verdict = "viable"
	} else {
		c.Verdict = "reject"
	}
	return c
}
