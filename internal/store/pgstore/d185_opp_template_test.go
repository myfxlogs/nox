package pgstore

import (
	"context"
	"testing"
)

// TestD185_OppTemplateSeed：迁移 188 种入 ≥8 模板、条件清单非空、执行轨枚举合法。
func TestD185_OppTemplateSeed(t *testing.T) {
	s := sharedTestStore(t)
	ts, err := s.ListOppTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) < 8 {
		t.Fatalf("模板册应 ≥8 族, got %d", len(ts))
	}
	valid := map[string]bool{"auto": true, "semi": true, "proposal": true}
	var coveredCall *OppTemplate
	for i := range ts {
		if !valid[ts[i].ExecTrack] {
			t.Fatalf("非法执行轨 %q @%s", ts[i].ExecTrack, ts[i].ID)
		}
		if len(ts[i].Conds) == 0 {
			t.Fatalf("模板 %s 条件清单空", ts[i].ID)
		}
		if ts[i].ID == "dci_sell_high_covered_call" {
			coveredCall = &ts[i]
		}
	}
	// covered-call 条件与 D-171 实测约束对拍：BTC 持仓+份额上限+随时赎。
	if coveredCall == nil {
		t.Fatal("covered-call 模板缺")
	}
	var hasHolding, hasShare, hasRedeem bool
	for _, c := range coveredCall.Conds {
		if c.Kind == "holding" && c.Subject == "BTC" && c.MinVal != nil && *c.MinVal > 0 {
			hasHolding = true
		}
		if c.Kind == "constraint" && c.Subject == "custody_share" {
			hasShare = true
		}
		if c.Kind == "constraint" && c.Subject == "custody_redeemable" {
			hasRedeem = true
		}
	}
	if !hasHolding || !hasShare || !hasRedeem {
		t.Fatalf("covered-call 条件与 D-171 约束不符: %+v", coveredCall.Conds)
	}
}
