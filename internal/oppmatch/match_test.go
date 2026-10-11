package oppmatch

import "testing"

func f(v float64) *float64 { return &v }

var testLedger = LedgerView{
	Credentials: map[string]bool{"okx:perp": true, "gate:perp": true},
	CapitalUSD:  10000,
	Holdings:    map[string]float64{"BTC": 0.05},
	Constraints: map[string]bool{"custody_share": true, "custody_redeemable": true},
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
