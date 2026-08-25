package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestListSettlementsByPosIDs D-106：批量查结算行（ListPositions join 用，单条 SQL IN 防 N+1）。
// 插 3 持仓 + 2 结算行 → 查 3 IDs → 返回 map 含 2 条，第 3 条不在 map 中（无结算行=缺省）。
func TestListSettlementsByPosIDs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_settlements CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 建 3 持仓。
	var ids []int64
	for i := 0; i < 3; i++ {
		var id int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'CLOSED', now()) RETURNING id`,
			"D106SET:SYM:vA:vB:"+string(rune('A'+i))).Scan(&id); err != nil {
			t.Fatalf("insert position %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	// 给 id[0] 和 id[1] 插结算行；id[2] 不插（无结算行=缺省）。
	notional := 10000.0
	for i := 0; i < 2; i++ {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, notional_usd, hold_days, settled_at)
			VALUES ($1, 'SWAPARB:vA:vB', 50.0, 30.0, $2, 5.5, $3)
		`, ids[i], &notional, time.Now()); err != nil {
			t.Fatalf("insert settlement %d: %v", i, err)
		}
	}

	got, err := s.ListSettlementsByPosIDs(ctx, ids)
	if err != nil {
		t.Fatalf("ListSettlementsByPosIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 settlements, got %d", len(got))
	}
	if _, ok := got[ids[0]]; !ok {
		t.Errorf("pos %d (id[0]) should have settlement", ids[0])
	}
	if _, ok := got[ids[1]]; !ok {
		t.Errorf("pos %d (id[1]) should have settlement", ids[1])
	}
	if _, ok := got[ids[2]]; ok {
		t.Errorf("pos %d (id[2]) should NOT have settlement (缺省)", ids[2])
	}
	// 验证字段值。
	sr := got[ids[0]]
	if sr.RealizedUsd != 30.0 {
		t.Errorf("realized_usd want 30.0, got %.4f", sr.RealizedUsd)
	}
	if sr.HoldDays != 5.5 {
		t.Errorf("hold_days want 5.5, got %.4f", sr.HoldDays)
	}
	if sr.NotionalUsd == nil || *sr.NotionalUsd != 10000.0 {
		t.Errorf("notional_usd want 10000.0, got %v", sr.NotionalUsd)
	}
}

// TestListSettlementsByPosIDs_Empty D-106：空 posIDs → 空 map（不查库）。
func TestListSettlementsByPosIDs_Empty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	got, err := s.ListSettlementsByPosIDs(ctx, nil)
	if err != nil {
		t.Fatalf("empty posIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty posIDs want 0, got %d", len(got))
	}
}
