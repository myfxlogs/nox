package oppmatch

import "testing"

func f(v float64) *float64 { return &v }

var testLedger = LedgerView{
	Credentials: map[string]bool{"okx:perp": true, "gate:perp": true},
	CapitalUSD:  10000,
	Holdings:    map[string]float64{"BTC": 0.05},
	Constraints: map[string]ConstraintVal{
		"custody_share":      {Qualifier: "max", Value: f(0.34)},
		"custody_redeemable": {Qualifier: "always"},
	},
	Permissions: map[string]bool{"dual_order": true},
}

// TestD187_MatchClassify：假账本×三模板→满足者提案/缺条件者缺口，分类正确不静默。
func TestD187_MatchClassify(t *testing.T) {
	ts := []Template{
		{ID: "funding_carry", ExecTrack: "auto", APR: f(0.12), Conds: []Cond{
			{Kind: "credential", Subject: "okx:perp"},
			{Kind: "capital", Subject: "USDT", MinVal: f(3000)},
			{Kind: "constraint", Subject: "cex_exposure_pct"},
		}},
		{ID: "dci_sell_high_covered_call", ExecTrack: "proposal", APR: f(0.25), Conds: []Cond{
			{Kind: "holding", Subject: "BTC", MinVal: f(0.01)},
			{Kind: "permission", Subject: "dual_order"},
			{Kind: "constraint", Subject: "custody_share"},
			{Kind: "constraint", Subject: "custody_redeemable"},
		}},
		{ID: "option_spread", ExecTrack: "proposal", Conds: []Cond{
			{Kind: "credential", Subject: "okx:option"},
			{Kind: "capital", Subject: "USDT", MinVal: f(2000)},
		}},
	}
	res := Match(ts, testLedger)
	var fund, cc, opt *Result
	for i := range res {
		switch res[i].TemplateID {
		case "funding_carry":
			fund = &res[i]
		case "dci_sell_high_covered_call":
			cc = &res[i]
		case "option_spread":
			opt = &res[i]
		}
	}
	if len(fund.Missing) != 1 || fund.Missing[0].Subject != "cex_exposure_pct" {
		t.Fatalf("funding_carry 应缺 cex_exposure_pct: %+v", fund.Missing)
	}
	if len(cc.Missing) != 0 {
		t.Fatalf("covered-call 应全满足: %+v", cc.Missing)
	}
	if len(opt.Missing) != 1 || opt.Missing[0].Kind != "credential" {
		t.Fatalf("option_spread 应缺凭据: %+v", opt.Missing)
	}
	// 排序：唯一提案者 covered-call 居首。
	if res[0].TemplateID != "dci_sell_high_covered_call" {
		t.Fatalf("提案组应居前: %s", res[0].TemplateID)
	}
}

// TestD187_MatchRankAPR：多提案按净 APR 降序、报价缺殿后。
func TestD187_MatchRankAPR(t *testing.T) {
	ts := []Template{
		{ID: "a", ExecTrack: "proposal", APR: f(0.05)},
		{ID: "b", ExecTrack: "semi", APR: f(0.20)},
		{ID: "c", ExecTrack: "auto", APR: nil},
	}
	res := Match(ts, testLedger)
	if res[0].TemplateID != "b" || res[1].TemplateID != "a" || res[2].TemplateID != "c" {
		t.Fatalf("APR 降序+nil殿后: %v", []string{res[0].TemplateID, res[1].TemplateID, res[2].TemplateID})
	}
}

// TestD187_MissingNotSilent：未知 kind=fail-closed 缺口（禁静默通过）。
func TestD187_MissingNotSilent(t *testing.T) {
	ts := []Template{{ID: "x", ExecTrack: "proposal", Conds: []Cond{{Kind: "magic", Subject: "y"}}}}
	res := Match(ts, testLedger)
	if len(res[0].Missing) != 1 {
		t.Fatal("未知 kind 应判缺口 fail-closed")
	}
}

// TestD188_CredTypeLevel：subject=venue_type 级凭据（mt5/futures/option）type:<vt> 键命中——
// 假缺口清零锚（生产实证 cfd_swap_carry「请补 mt5 凭据」但 broker_accounts 有 5 个 mt5）。
func TestD188_CredTypeLevel(t *testing.T) {
	l := LedgerView{
		Credentials: map[string]bool{"type:mt5": true, "type:perp": true},
		Constraints: map[string]ConstraintVal{}, Permissions: map[string]bool{},
		CapitalBy: map[string]float64{}, Holdings: map[string]float64{},
	}
	if !EvalCond(l, Cond{Kind: "credential", Subject: "mt5"}) {
		t.Fatal("type:mt5 级凭据应命中")
	}
	if EvalCond(l, Cond{Kind: "credential", Subject: "option"}) {
		t.Fatal("option type 凭据无应缺口")
	}
}

// TestD188_ConstraintCompare：constraint 限值数值比较（max/min+value_num）。
func TestD188_ConstraintCompare(t *testing.T) {
	l := LedgerView{Constraints: map[string]ConstraintVal{
		"cex_exposure_pct": {Qualifier: "max", Value: f(35)},
		"reserve_pct":      {Qualifier: "min", Value: f(30)},
	}, Credentials: map[string]bool{}, CapitalBy: map[string]float64{},
		Holdings: map[string]float64{}, Permissions: map[string]bool{}}
	// max 比较：约束值 ≤ cond 界=满足。
	if !EvalCond(l, Cond{Kind: "constraint", Subject: "cex_exposure_pct", Qualifier: "max", MinVal: f(40)}) {
		t.Fatal("max 约束 35≤40 应满足")
	}
	if EvalCond(l, Cond{Kind: "constraint", Subject: "cex_exposure_pct", Qualifier: "max", MinVal: f(30)}) {
		t.Fatal("max 约束 35>30 应缺口（约束不够严）")
	}
	if !EvalCond(l, Cond{Kind: "constraint", Subject: "reserve_pct", Qualifier: "min", MinVal: f(25)}) {
		t.Fatal("min 约束 30≥25 应满足")
	}
	// 无数值约束只验存在。
	if !EvalCond(l, Cond{Kind: "constraint", Subject: "cex_exposure_pct"}) {
		t.Fatal("无 MinVal 应只验存在")
	}
}

// TestD188_PermissionEval：探测面 present→permission 条件可求值。
func TestD188_PermissionEval(t *testing.T) {
	l := LedgerView{Permissions: map[string]bool{"dual_order": true},
		Credentials: map[string]bool{}, Constraints: map[string]ConstraintVal{},
		CapitalBy: map[string]float64{}, Holdings: map[string]float64{}}
	if !EvalCond(l, Cond{Kind: "permission", Subject: "dual_order"}) {
		t.Fatal("探测 present 权限应满足")
	}
	if EvalCond(l, Cond{Kind: "permission", Subject: "earn_subscribe"}) {
		t.Fatal("未探得 earn_subscribe 应缺口")
	}
}
