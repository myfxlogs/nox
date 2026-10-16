package pgstore

// position_strategy_key_test.go —— D-577 归组键锚（退回补正后口径）：
// 判据表 source_kind∈{structure,engine_type}；provenance 精确键优先、
// engine_type 回落、全解不开→NULL（读面 unassigned）。

import (
	"context"
	"fmt"
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

// TestCreatePosition_StrategyKey_Provenance 主径锚（收口挂账销）：种真库
// opportunity+enabled 'D-403毕业 paper#N' combo+paper.structure+map 行 →
// strategy_key 命中 structure 精确键（同名 engine_type 键也在册=优先级断言）；
// 负例=非毕业 note combo 不解 provenance→回落 engine_type。
func TestCreatePosition_StrategyKey_Provenance(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	clean := []string{
		`DELETE FROM fin_position_strategy_map`,
		`DELETE FROM positions WHERE opportunity_id LIKE 'SGP%'`,
		`DELETE FROM opportunities WHERE id LIKE 'SGP%'`,
		`DELETE FROM xasset_allow_combos WHERE canonical_buy='SGPX'`,
		`DELETE FROM paper_spot_carry WHERE symbol='SGPX'`,
	}
	for _, q := range clean {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, q := range clean {
			_, _ = s.pool.Exec(context.Background(), q)
		}
	}()

	// paper.structure=策略定义面（provenance 终链节）。
	var paperID int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO paper_spot_carry (venue_a, venue_b, symbol, structure, side, qty_usd, status, entry_ts, entry_funding_ann)
		 VALUES ('gate','binance','SGPX','long_cfd_short_perp','short_perp',1000,'active',now(),10) RETURNING id`).Scan(&paperID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO opportunities (id, type, venue_buy, venue_sell, price_buy, price_sell, gross_bps, net_bps, quote_time, expires_at, canonical)
		 VALUES ('SGP1','CARRY','gate','binance',1,1,10,9,now(),now()+interval '1 hour','SGPX'),
		        ('SGP2','CARRY','okx','binance',1,1,10,9,now(),now()+interval '1 hour','SGPX')`,
		`INSERT INTO fin_position_strategy_map (source_kind, source_val, strategy_key) VALUES
		 ('engine_type','CARRY','carry_generic'),('structure','long_cfd_short_perp','cfd_carry')`,
		// SGP1 命中毕业 combo（venue 对=gate×binance）；SGP2 指向非毕业 note combo（不解链）。
		`INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note) VALUES
		 ('SGPX','SGPX','gate','perp','binance','perp',1,1,100,true,false,'D-403毕业 paper#` + itoa(paperID) + `'),
		 ('SGPX','SGPX','okx','perp','binance','perp',1,1,100,true,false,'S2桥 trial')`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	pid1, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGP1", Type: "CARRY", Status: "OPENING"})
	if err != nil {
		t.Fatal(err)
	}
	pid2, err := s.CreatePosition(ctx, &Position{OpportunityID: "SGP2", Type: "CARRY", Status: "OPENING"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPositions(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]*Position{}
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	if k := byID[pid1].StrategyKey; k == nil || *k != "cfd_carry" {
		t.Fatalf("provenance 主径应命中 structure 键 cfd_carry, got %+v", k)
	}
	if k := byID[pid2].StrategyKey; k == nil || *k != "carry_generic" {
		t.Fatalf("非毕业 note 应回落 engine_type 键 carry_generic, got %+v", k)
	}
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }
