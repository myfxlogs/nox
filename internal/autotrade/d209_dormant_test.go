package autotrade

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

// TestD209_XAssetDormantKey 键粒度对拍——canonical 剥 USD/USDT 后缀、venue 裸端点归一。
func TestD209_XAssetDormantKey(t *testing.T) {
	cases := map[string]string{ // (canonical,venue) → key
		"XAUUSD|okx:perp":  "xasset_carry:XAU@okx",
		"XAUUSDT|okx":      "xasset_carry:XAU@okx",
		"XAGUSD|okx:perp":  "xasset_carry:XAG@okx",
		"XAUUSD|ic:mt5":    "xasset_carry:XAU@ic",
		"XPTUSD|gate:perp": "xasset_carry:XPT@gate",
	}
	for in, want := range cases {
		parts := strings.SplitN(in, "|", 2)
		if got := pgstore.XAssetDormantKey(parts[0], parts[1]); got != want {
			t.Fatalf("XAssetDormantKey(%s,%s)=%q 期望 %q", parts[0], parts[1], got, want)
		}
	}
}

// TestD209_EngineEvalDormant 真库锚——约束行在→eval 拒 XAU@okx+info 台账行；
// XAG@okx/XAU@gate 不误伤；value_num=0/nil 不生效。
func TestD209_EngineEvalDormant(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	defer s.Close()
	d209Clean(t, s, ctx)
	one := 1.0
	zero := 0.0
	must := func(c pgstore.CapabilityConstraint) {
		t.Helper()
		if err := s.UpsertCapabilityConstraint(ctx, &c); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	must(pgstore.CapabilityConstraint{Scope: "bucket", Subject: "dormant:xasset_carry:XAU@okx", Qualifier: "always", ValueNum: &one})
	must(pgstore.CapabilityConstraint{Scope: "bucket", Subject: "dormant:xasset_carry:XPT@okx", Qualifier: "always", ValueNum: &zero})  // 关
	must(pgstore.CapabilityConstraint{Scope: "bucket", Subject: "unrelated:xasset_carry:XAU@okx", Qualifier: "always", ValueNum: &one}) // 非前缀

	e := &Engine{store: s}
	oppXAUokx := pgstore.Opportunity{ID: "o1", Type: "XASSET_CARRY", Canonical: "XAUUSD",
		VenueBuy: "ic:mt5", VenueSell: "okx:perp"}
	if !e.xassetDormantSkip(ctx, &oppXAUokx) {
		t.Fatal("XAU@okx 休眠命中应 skip")
	}
	alerts, err := s.ListAlerts(ctx, 50, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	var hit bool
	for _, a := range alerts {
		if a.Level == "info" && strings.Contains(a.Message, "休眠轨（D-209）xasset_carry:XAU@okx 跳过提案") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("info 台账行缺，alerts=%+v", alerts)
	}
	// 粒度不误伤
	for _, o := range []pgstore.Opportunity{
		{ID: "o2", Canonical: "XAGUSD", VenueBuy: "ic:mt5", VenueSell: "okx:perp"},
		{ID: "o3", Canonical: "XAUUSD", VenueBuy: "ic:mt5", VenueSell: "gate:perp"},
		{ID: "o4", Canonical: "XPTUSD", VenueBuy: "ic:mt5", VenueSell: "okx:perp"}, // value_num=0
	} {
		if e.xassetDormantSkip(ctx, &o) {
			t.Fatalf("%s 不该命中休眠", o.ID)
		}
	}
	// 双腿 venue 各查——休眠键命中买腿侧 venue
	oppBuyHit := pgstore.Opportunity{ID: "o5", Canonical: "XAUUSD", VenueBuy: "okx:spot", VenueSell: "ic:mt5"}
	if !e.xassetDormantSkip(ctx, &oppBuyHit) {
		t.Fatal("休眠键命中买腿 venue 应 skip")
	}
}

// TestD209_DormantAbsentPasses 约束行缺→放行（不 skip）。
// d209Clean 共享测试库约束行归位——dormant:xasset_carry:* 前缀行全清（幂等，
// 防跨测试残留；同 D-204 id=10 生产种子语义，测试库亦带迁移种子 dormant:* 行须防）。
func d209Clean(t *testing.T, s *pgstore.Store, ctx context.Context) {
	t.Helper()
	cons, err := s.ListCapabilityConstraints(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range cons {
		if strings.HasPrefix(c.Subject, "dormant:") {
			if err := s.DeleteCapabilityConstraint(ctx, c.ID); err != nil {
				t.Fatalf("delete %d: %v", c.ID, err)
			}
		}
	}
}

func TestD209_DormantAbsentPasses(t *testing.T) {
	s := testAccumStore(t)
	defer s.Close()
	d209Clean(t, s, context.Background())
	e := &Engine{store: s}
	opp := pgstore.Opportunity{ID: "o6", Canonical: "XAUUSD", VenueBuy: "ic:mt5", VenueSell: "okx:perp"}
	if e.xassetDormantSkip(context.Background(), &opp) {
		t.Fatal("无休眠约束应放行")
	}
}
