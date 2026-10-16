package pgstore

// position_strategy_key_test.go —— D-577 归组键锚（退回补正后口径）：
// 判据表 source_kind∈{structure,engine_type}；provenance 精确键优先、
// engine_type 回落、全解不开→NULL（读面 unassigned）。

import (
	"context"
	"testing"
)

func TestCreatePosition_StrategyKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_position_strategy_map; DELETE FROM positions WHERE opportunity_id LIKE 'SG%' `); err != nil {
		t.Fatal(err)
	}
	// engine_type 回落键：CARRY→funding_carry。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_position_strategy_map (source_kind, source_val, strategy_key)
		 VALUES ('engine_type','CARRY','funding_carry'),('structure','long_cfd_short_perp','cfd_carry')`); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGK1", Type: "CARRY", Status: "OPENING"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id2, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGU1", Type: "PROBE", Status: "OPENING"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := s.ListPositions(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]*Position{}
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	if k := byID[id].StrategyKey; k == nil || *k != "funding_carry" {
		t.Fatalf("engine_type 回落应=funding_carry, got %+v", k)
	}
	if byID[id2].StrategyKey != nil {
		t.Fatalf("全解不开应 NULL=unassigned, got %+v", *byID[id2].StrategyKey)
	}
}
