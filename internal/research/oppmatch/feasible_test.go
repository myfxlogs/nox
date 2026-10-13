// feasible_test.go —— D-247：可行性预检锚（纯函数无 DB）。
package oppmatch

import (
	"testing"

	"nox/internal/store/pgstore"
)

func f64v(v float64) *float64 { return &v }

// TestD247_Feasibility 三谓词：占用超单策略上限 / 突破储备约束 / venue 不在账本。
func TestD247_Feasibility(t *testing.T) {
	led := &pgstore.CapabilityLedger{
		Credentials: []pgstore.VenueCredentialCap{{Venue: "binance", VenueType: "futures"}},
		Buckets:     []pgstore.FundBucket{{Venue: "binance"}},
		Constraints: []pgstore.CapabilityConstraint{
			{Subject: "reserve_pct", ValueNum: f64v(30)},
		},
	}
	f := NewFeasibility(led, 10000, 1000)

	// #10 型：权益 ≥10000 USD=整基金 → 双谓词均不可行。
	ok, reason := f.Check(Result{TemplateID: "t", Missing: []Cond{
		{Kind: "capital", Subject: "USD", MinVal: f64v(10000)}}})
	if ok || reason == "" {
		t.Fatalf("整基金请求应不可行: ok=%v reason=%q", ok, reason)
	}
	// 超单策略上限未破储备（2000>1000 cap）→ 不可行。
	if ok, _ := f.Check(Result{Missing: []Cond{
		{Kind: "capital", Subject: "USD", MinVal: f64v(2000)}}}); ok {
		t.Fatal("2000>cap1000 应不可行")
	}
	// 界内请求（500≤cap）→ 可行。
	if ok, r := f.Check(Result{Missing: []Cond{
		{Kind: "capital", Subject: "USD", MinVal: f64v(500)}}}); !ok {
		t.Fatalf("500 界内应可行: %s", r)
	}
	// venue 不在账本 → 不可行。
	if ok, _ := f.Check(Result{Missing: []Cond{
		{Kind: "credential", Subject: "kraken"}}}); ok {
		t.Fatal("未知 venue 应不可行")
	}
	// venue 在账本（binance / type:futures 双键）→ 可行。
	if ok, r := f.Check(Result{Missing: []Cond{
		{Kind: "credential", Subject: "binance"}}}); !ok {
		t.Fatalf("binance 在账本应可行: %s", r)
	}
	if ok, r := f.Check(Result{Missing: []Cond{
		{Kind: "credential", Subject: "futures"}}}); !ok {
		t.Fatalf("type 级凭据应可行: %s", r)
	}
	// cap≤0（未配置）→ 第一谓词失效不拦（储备谓词仍在）。
	f2 := NewFeasibility(led, 10000, 0)
	if ok, _ := f2.Check(Result{Missing: []Cond{
		{Kind: "capital", Subject: "USD", MinVal: f64v(2000)}}}); !ok {
		t.Fatal("cap 未配置不应拦 2000（储备 30% 界 7000 未破）")
	}
	// 储备谓词：8000 > 10000×(1-30%)=7000 → 不可行（cap=0 让第一谓词失效）。
	if ok, _ := f2.Check(Result{Missing: []Cond{
		{Kind: "capital", Subject: "USD", MinVal: f64v(8000)}}}); ok {
		t.Fatal("8000 破储备约束应不可行")
	}
}

// TestD247_DormantConstraint 休眠行键规约（D-204 同式 dormant:<tmpl>@<venue>）。
func TestD247_DormantConstraint(t *testing.T) {
	c := DormantConstraint("dci_buy_low", "binance", "测试原因")
	if c.Subject != "dormant:dci_buy_low@binance" || c.Source != "m40-precheck" ||
		c.ValueNum == nil || *c.ValueNum != 1 || c.Note != "测试原因" {
		t.Fatalf("dormant 行面错: %+v", c)
	}
	if !pgstore.DormantPairs([]pgstore.CapabilityConstraint{*c})["dci_buy_low@binance"] {
		t.Fatal("DormantPairs 应命中")
	}
}
