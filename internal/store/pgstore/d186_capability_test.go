package pgstore

import (
	"context"
	"testing"
)

// TestD186_ConstraintCRUD：约束行增删查+种子托管约束落账本。
func TestD186_ConstraintCRUD(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 0189 种子三行（其他测试 TRUNCATE 可抹——自补幂等重放同义行，包内先例）。
	third := 1.0 / 3
	seeds := []CapabilityConstraint{
		{Scope: "bucket", Subject: "custody_share", Qualifier: "max", ValueNum: &third,
			Note: "BTC 托管可动份额上限", Source: "D-171 业主令"},
		{Scope: "bucket", Subject: "custody_redeemable", Qualifier: "always",
			Note: "托管币随时可赎回", Source: "D-171 业主令"},
		{Scope: "bucket", Subject: "custody_settlement", Qualifier: "current_price",
			Note: "托管线现价结算口径", Source: "D-171 业主令"},
	}
	for i := range seeds {
		if err := s.UpsertCapabilityConstraint(ctx, &seeds[i]); err != nil {
			t.Fatal(err)
		}
	}
	cons, err := s.ListCapabilityConstraints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var share, redeem *CapabilityConstraint
	for i := range cons {
		if cons[i].Subject == "custody_share" {
			share = &cons[i]
		}
		if cons[i].Subject == "custody_redeemable" {
			redeem = &cons[i]
		}
	}
	if share == nil || redeem == nil {
		t.Fatalf("托管约束种子缺: %+v", cons)
	}
	if share.ValueNum == nil || *share.ValueNum < 0.30 || *share.ValueNum > 0.40 {
		t.Fatalf("custody_share 应≈1/3, got %v", share.ValueNum)
	}
	// upsert 幂等+改 qualifier。
	c := &CapabilityConstraint{Scope: "global", Subject: "reserve_pct",
		Qualifier: "min", ValueNum: share.ValueNum, Note: "储备底线", Source: "test"}
	if err := s.UpsertCapabilityConstraint(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Qualifier = "max"
	if err := s.UpsertCapabilityConstraint(ctx, c); err != nil {
		t.Fatal(err)
	}
	cons, _ = s.ListCapabilityConstraints(ctx)
	n := 0
	var id int64
	for _, cc := range cons {
		if cc.Subject == "reserve_pct" {
			n++
			id = cc.ID
			if cc.Qualifier != "max" {
				t.Fatalf("upsert 未改 qualifier: %+v", cc)
			}
		}
	}
	if n != 1 {
		t.Fatalf("upsert 应幂等单行, got %d", n)
	}
	if err := s.DeleteCapabilityConstraint(ctx, id); err != nil {
		t.Fatal(err)
	}
	cons, _ = s.ListCapabilityConstraints(ctx)
	for _, cc := range cons {
		if cc.Subject == "reserve_pct" {
			t.Fatal("delete 未生效")
		}
	}
}

// TestD186_ConditionGap：缺口登记幂等→请求态机→飞书链（alert 行承载）→收口。
func TestD186_ConditionGap(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 测试库行跨轮残留（sharedTestStore 同库）——先清场保幂等。
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM fin_condition_gap WHERE subject='d186_gap_subj'`); err != nil {
		t.Fatal(err)
	}
	g := &ConditionGap{TemplateID: "dci_sell_high_covered_call", Kind: "permission",
		Subject: "d186_gap_subj", RequestText: "请开通 gate 双币下单权限"}
	id, err := s.OpenConditionGap(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.OpenConditionGap(ctx, g)
	if err != nil || id2 != id {
		t.Fatalf("重复登记应幂等同 id, id=%v id2=%v err=%v", id, id2, err)
	}
	if err := s.RequestConditionGap(ctx, id); err != nil {
		t.Fatal(err)
	}
	gaps, _ := s.ListConditionGaps(ctx, "requested")
	found := false
	for _, gg := range gaps {
		if gg.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("requested 态缺")
	}
	// 飞书链锚：alert 行携缺口文案（episode 闸在 wire 层，链证=行已落）。
	var cnt int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%d186_gap_subj%'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt == 0 {
		t.Fatal("缺口告警行未落（飞书链断）")
	}
	if err := s.RequestConditionGap(ctx, id); err != nil {
		t.Fatal(err)
	}
	var cnt2 int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%d186_gap_subj%'`).Scan(&cnt2)
	if cnt2 != cnt {
		t.Fatalf("requested 态再请求应零推, %d→%d", cnt, cnt2)
	}
	if err := s.ResolveConditionGap(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	gaps, _ = s.ListConditionGaps(ctx, "fulfilled")
	for _, gg := range gaps {
		if gg.ID == id {
			return
		}
	}
	t.Fatal("fulfilled 态缺")
}

// TestD186_LedgerAggregate：聚合视图正确性——凭据只记有无零明文、桶/持仓/约束/缺口齐面。
func TestD186_LedgerAggregate(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	var has bool
	s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM exchange_configs WHERE venue='d186venue')`).Scan(&has)
	if !has {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret)
			VALUES ('d186venue','spot','k','sec')`); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.ListCapabilityLedger(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cred *VenueCredentialCap
	for i := range l.Credentials {
		if l.Credentials[i].Venue == "d186venue" {
			cred = &l.Credentials[i]
		}
	}
	if cred == nil {
		t.Fatal("凭据面未聚合 exchange_configs 行")
	}
	if len(cred.Tags) == 0 || cred.Tags[0] != "cex" {
		t.Fatalf("能力标签缺: %+v", cred.Tags)
	}
	// 约束/缺口进聚合面。
	found := false
	for _, c := range l.Constraints {
		if c.Subject == "custody_share" {
			found = true
		}
	}
	if !found {
		t.Fatal("聚合视图缺约束面")
	}
}
