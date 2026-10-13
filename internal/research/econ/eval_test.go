package econ

import "testing"

// TestM41_Evaluate 三终态+缺证据一票否决+机理 n/a 证词算证据。
func TestM41_Evaluate(t *testing.T) {
	full := func(g, fee, slip, fail, win float64) EvalInput {
		return EvalInput{TemplateID: "dci_buy_low",
			Gross: Term{g, "quote"}, Fee: Term{fee, "taker"},
			Slippage: Term{slip, "spread"}, Failure: Term{fail, "ledger"},
			Window: Term{win, "median"}}
	}
	if c := Evaluate(full(10, 1, 0.5, 0, 2)); c.Verdict != "viable" || c.Net != 6.5 {
		t.Fatalf("viable: %+v", c)
	}
	if c := Evaluate(full(3, 1, 0.5, 0, 2)); c.Verdict != "reject" || c.Net != -0.5 {
		t.Fatalf("reject: %+v", c)
	}
	// 任一 Evidence 空→unevaluable（净符号不外推）。
	in := full(100, 1, 0.5, 0, 2)
	in.Failure = Term{}
	c := Evaluate(in)
	if c.Verdict != "unevaluable" || len(c.Missing) != 1 || c.Missing[0] != "failure" {
		t.Fatalf("unevaluable: %+v", c)
	}
	// 毛报价缺席同判——788 型无报价证据不外推。
	in = full(0, 1, 0.5, 0, 2)
	in.Gross = Term{}
	if c := Evaluate(in); c.Verdict != "unevaluable" {
		t.Fatalf("gross missing: %+v", c)
	}
	// mechanism: 证词=有证据（值 0）计 viable 面。
	in = full(8, 0, 0, 0, 0)
	in.Fee = Term{0, "mechanism:DCI申购费不适用"}
	c = Evaluate(in)
	if c.Verdict != "viable" || c.Net != 8 {
		t.Fatalf("mechanism n/a: %+v", c)
	}
}
