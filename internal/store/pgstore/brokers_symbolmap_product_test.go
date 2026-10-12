package pgstore

import (
	"context"
	"testing"
)

// DB 集成测试：ListProductGroups 众数聚合 + product_group 配对场景。
// 需 NOX_TEST_PG_DSN，未设置时 skip（testStore 门禁）。
// 覆盖 470 事故根因：同 canonical BRENT 但 FxPro=cfd_spot × PXBT=cfd_leverage → 不同 product_group 被滤。

// 用例①：同 product_group 通过——两 venue 同 canonical 同 product_group。
func TestListProductGroupsSamePass(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "529", "277", "fxpro", "pxbt", "v1")
	// 两 venue 同 canonical AUDJPY，product_group=fx
	if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "529", BrokerSymbol: "AUDJPYm", Canonical: "AUDJPY", Source: "manual"}); err != nil {
		t.Fatalf("upsert 529: %v", err)
	}
	if n, err := s.UpdateProductGroupByCanonical(ctx, "529", "AUDJPY", "fx"); err != nil || n != 1 {
		t.Fatalf("update pg 529: n=%d err=%v", n, err)
	}
	if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "277", BrokerSymbol: "AUDJPYm", Canonical: "AUDJPY", Source: "manual"}); err != nil {
		t.Fatalf("upsert 277: %v", err)
	}
	if n, err := s.UpdateProductGroupByCanonical(ctx, "277", "AUDJPY", "fx"); err != nil || n != 1 {
		t.Fatalf("update pg 277: n=%d err=%v", n, err)
	}

	pgMap, err := s.ListProductGroups(ctx)
	if err != nil {
		t.Fatalf("ListProductGroups: %v", err)
	}
	longPG := pgMap[VenueCanonKey{Venue: "529", Canonical: "AUDJPY"}]
	shortPG := pgMap[VenueCanonKey{Venue: "277", Canonical: "AUDJPY"}]
	if longPG != "fx" {
		t.Errorf("529/AUDJPY product_group: got %q, want fx", longPG)
	}
	if shortPG != "fx" {
		t.Errorf("277/AUDJPY product_group: got %q, want fx", shortPG)
	}
	// 同 product_group → 配对通过（sameProductGroup=true 语义）
	if longPG != shortPG {
		t.Errorf("same product_group should pass: long=%q != short=%q", longPG, shortPG)
	}
}

// 用例②：不同 product_group 被滤——470 场景。
// FxPro BRENT CalcMode=2(cfd_spot) × PXBT BRENT CalcMode=4(cfd_leverage)。
func TestListProductGroups470Filtered(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "529", "277", "fxpro", "pxbt", "v1")
	// FxPro BRENT → cfd_spot
	if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "fxpro", BrokerSymbol: "BRENT", Canonical: "BRENT", Source: "manual"}); err != nil {
		t.Fatalf("upsert fxpro: %v", err)
	}
	if n, err := s.UpdateProductGroupByCanonical(ctx, "fxpro", "BRENT", "cfd_spot"); err != nil || n != 1 {
		t.Fatalf("update pg fxpro: n=%d err=%v", n, err)
	}
	// PXBT BRENT → cfd_leverage
	if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "pxbt", BrokerSymbol: "BRENT", Canonical: "BRENT", Source: "manual"}); err != nil {
		t.Fatalf("upsert pxbt: %v", err)
	}
	if n, err := s.UpdateProductGroupByCanonical(ctx, "pxbt", "BRENT", "cfd_leverage"); err != nil || n != 1 {
		t.Fatalf("update pg pxbt: n=%d err=%v", n, err)
	}

	pgMap, err := s.ListProductGroups(ctx)
	if err != nil {
		t.Fatalf("ListProductGroups: %v", err)
	}
	longPG := pgMap[VenueCanonKey{Venue: "fxpro", Canonical: "BRENT"}]
	shortPG := pgMap[VenueCanonKey{Venue: "pxbt", Canonical: "BRENT"}]
	if longPG != "cfd_spot" {
		t.Errorf("fxpro/BRENT product_group: got %q, want cfd_spot", longPG)
	}
	if shortPG != "cfd_leverage" {
		t.Errorf("pxbt/BRENT product_group: got %q, want cfd_leverage", shortPG)
	}
	// 470 根因：同 canonical 但不同 product_group → 配对被滤（sameProductGroup=false 语义）
	if longPG == shortPG {
		t.Errorf("470 scenario: product_groups should differ (long=%q == short=%q), must be filtered", longPG, shortPG)
	}
}

// 用例③：众数聚合——同 (venue, canonical) 多行 product_group 取众数。
func TestListProductGroupsModeAggregation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "529", "277", "fxpro", "pxbt", "v1")
	// 同 venue+canonical 三行：两行 cfd_spot、一行 cfd_leverage → 众数 cfd_spot
	for _, bs := range []string{"BRENT.a", "BRENT.b", "BRENT.c"} {
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "v1", BrokerSymbol: bs, Canonical: "BRENT", Source: "manual"}); err != nil {
			t.Fatalf("upsert %s: %v", bs, err)
		}
	}
	// 直接写 product_group 制造众数场景（UpdateProductGroupByCanonical 按 venue+canonical 全量更新，
	// 无法制造多值；用原生 SQL 写不同 broker_symbol 行的不同 product_group）
	if _, err := s.pool.Exec(ctx, `
		UPDATE broker_symbol_map SET product_group = 'cfd_spot'
		WHERE venue = 'v1' AND broker_symbol IN ('BRENT.a', 'BRENT.b')
	`); err != nil {
		t.Fatalf("set cfd_spot: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE broker_symbol_map SET product_group = 'cfd_leverage'
		WHERE venue = 'v1' AND broker_symbol = 'BRENT.c'
	`); err != nil {
		t.Fatalf("set cfd_leverage: %v", err)
	}

	pgMap, err := s.ListProductGroups(ctx)
	if err != nil {
		t.Fatalf("ListProductGroups: %v", err)
	}
	pg := pgMap[VenueCanonKey{Venue: "v1", Canonical: "BRENT"}]
	if pg != "cfd_spot" {
		t.Errorf("mode aggregation: got %q, want cfd_spot (2 vs 1 majority)", pg)
	}
}
