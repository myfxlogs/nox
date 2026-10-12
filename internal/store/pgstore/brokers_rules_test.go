package pgstore

import (
	"context"
	"testing"

	"nox/internal/data/normalize"
)

// 单测：UpsertSymbolMappingCSOnly 仅填 NULL + 覆盖率四态。
// 需 NOX_TEST_PG_DSN，未设置时 skip。

// 用例①：upsert 仅填 NULL 行（已有非 NULL CS 不覆盖）
func TestUpsertCSOnlyFillsNull(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "test", "v1", "backfill test")
	// 插入 CS=NULL 行
	if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{
		Venue: "test", BrokerSymbol: "AUDJPY", Canonical: "AUDJPY", ContractSize: nil, Source: "manual",
	}); err != nil {
		t.Fatalf("insert null CS: %v", err)
	}
	// upsert CS=100000 → 应填 NULL
	filled, inserted, err := s.UpsertSymbolMappingCSOnly(ctx, "test", "AUDJPY", 100000)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if filled != 1 || inserted != 0 {
		t.Errorf("got filled=%d inserted=%d, want filled=1 inserted=0", filled, inserted)
	}
	// 验证 CS 已填
	maps, _ := s.ListSymbolMappings(ctx, "test")
	if maps[0].ContractSize == nil || *maps[0].ContractSize != 100000 {
		t.Errorf("CS not filled: %v", maps[0].ContractSize)
	}
	// 再次 upsert CS=200000 → 不覆盖（已有非 NULL）
	filled2, _, err := s.UpsertSymbolMappingCSOnly(ctx, "test", "AUDJPY", 200000)
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if filled2 != 0 {
		t.Errorf("second upsert should not fill (CS already non-NULL), got filled=%d", filled2)
	}
	maps2, _ := s.ListSymbolMappings(ctx, "test")
	if *maps2[0].ContractSize != 100000 {
		t.Errorf("CS should still be 100000, got %v", *maps2[0].ContractSize)
	}
}

// 用例②：upsert 不存在行 → 插入 source='meta'
func TestUpsertCSOnlyInsertsMeta(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "test", "v1", "backfill test")
	filled, inserted, err := s.UpsertSymbolMappingCSOnly(ctx, "test", "NEWUSD", 50000)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if filled != 0 || inserted != 1 {
		t.Errorf("got filled=%d inserted=%d, want filled=0 inserted=1", filled, inserted)
	}
	maps, _ := s.ListSymbolMappings(ctx, "test")
	if len(maps) != 1 {
		t.Fatalf("got %d rows, want 1", len(maps))
	}
	if maps[0].Source != "meta" {
		t.Errorf("source should be 'meta', got %q", maps[0].Source)
	}
	if maps[0].Canonical != "" {
		t.Errorf("canonical should be empty (pending), got %q", maps[0].Canonical)
	}
	if *maps[0].ContractSize != 50000 {
		t.Errorf("CS should be 50000, got %v", *maps[0].ContractSize)
	}
}

// 用例③：覆盖率四态
func TestSymbolCoverageFourStates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "test", "v1", "backfill test")
	// mapped+CS
	cs1 := 100000.0
	s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "v1", BrokerSymbol: "A", Canonical: "AUDJPY", ContractSize: &cs1, Source: "manual"})
	// mapped-no-CS
	s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "v1", BrokerSymbol: "B", Canonical: "BRENT", ContractSize: nil, Source: "manual"})
	// rule-only
	s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "v1", BrokerSymbol: "C", Canonical: "WTI", ContractSize: nil, Source: "rule"})
	// unmapped：服务器有 D 但 DB 无
	serverSyms := []string{"A", "B", "C", "D"}
	rows, err := s.SymbolCoverage(ctx, "v1", serverSyms)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	statuses := map[string]int{}
	for _, r := range rows {
		statuses[r.Status]++
	}
	if statuses["mapped+CS"] != 1 {
		t.Errorf("mapped+CS: got %d, want 1", statuses["mapped+CS"])
	}
	if statuses["mapped-no-CS"] != 1 {
		t.Errorf("mapped-no-CS: got %d, want 1", statuses["mapped-no-CS"])
	}
	if statuses["rule-only"] != 1 {
		t.Errorf("rule-only: got %d, want 1", statuses["rule-only"])
	}
	if statuses["unmapped"] != 1 {
		t.Errorf("unmapped: got %d, want 1 (server sym D has no mapping)", statuses["unmapped"])
	}
}

// TestBackfillRuleMappings_RejectStockSymbols D-176：反污染护栏拒绝含 . 的 broker_symbol（股票代码格式）。
// 传入 ["CLNE.NAS", "EURUSD"]，验证 CLNE.NAS 不入库 + EURUSD 入库。
func TestBackfillRuleMappings_RejectStockSymbols(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "test", "v1", "backfill test")
	eng := normalize.NewRuleEngine()
	inserted, err := s.BackfillRuleMappings(ctx, "test", []string{"CLNE.NAS", "EURUSD"}, eng)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted: got %d, want 1 (EURUSD only; CLNE.NAS rejected by guard)", inserted)
	}
	maps, _ := s.ListSymbolMappings(ctx, "test")
	gotSyms := map[string]bool{}
	for _, m := range maps {
		gotSyms[m.BrokerSymbol] = true
	}
	if gotSyms["CLNE.NAS"] {
		t.Error("CLNE.NAS should be rejected (contains '.'), but was inserted")
	}
	if !gotSyms["EURUSD"] {
		t.Error("EURUSD should be inserted (no '.', matches FX rule)")
	}
}
