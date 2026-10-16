package pgstore

// position_strategy_key_test.go —— D-577 归组键锚：map 有行→strategy_key 落列；
// map 无行→NULL（unassigned 语义由读面透出，写径如实 NULL 不猜挂）。

import (
	"context"
	"testing"
)

func TestCreatePosition_StrategyKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_position_strategy_map; DELETE FROM positions WHERE opportunity_id LIKE 'SGK%' OR opportunity_id LIKE 'SGU%'`); err != nil {
		t.Fatal(err)
	}
	// 判据落表：CARRY→funding_carry。
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_position_strategy_map (engine_type, strategy_key) VALUES ('CARRY','funding_carry')`); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGK1", Type: "CARRY", Status: "OPENING"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := s.ListPositions(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got *string
	for _, p := range rows {
		if p.ID == id {
			got = p.StrategyKey
		}
	}
	if got == nil || *got != "funding_carry" {
		t.Fatalf("strategy_key 应=funding_carry, got %+v", got)
	}
	// 无 map 行→NULL（未归组）。
	id2, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGU1", Type: "PROBE", Status: "OPENING"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var nul *string
	for _, p := range rows2(s, ctx, t) {
		if p.ID == id2 {
			nul = p.StrategyKey
		}
	}
	if nul != nil {
		t.Fatalf("map 无行应 NULL=unassigned, got %+v", *nul)
	}
}

func rows2(s *Store, ctx context.Context, t *testing.T) []Position {
	rows, err := s.ListPositions(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
