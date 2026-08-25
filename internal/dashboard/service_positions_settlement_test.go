package dashboard

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// fakeSettlementQuerier 测试用 settlementQuerier（不查库）。
type fakeSettlementQuerier struct {
	rows map[int64]*pgstore.SettlementRow
	err  error
}

func (f *fakeSettlementQuerier) ListSettlementsByPosIDs(ctx context.Context, posIDs []int64) (map[int64]*pgstore.SettlementRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// TestEnrichSettlements_WithSettlement D-106：有结算行 → 填充三字段。
func TestEnrichSettlements_WithSettlement(t *testing.T) {
	notional := 5000.0
	store := &fakeSettlementQuerier{rows: map[int64]*pgstore.SettlementRow{
		101: {PositionID: 101, RealizedUsd: 42.5, NotionalUsd: &notional, HoldDays: 3.2},
	}}
	pbPositions := []*v1.Position{
		{Id: 101, Type: "SWAPARB", Status: "CLOSED"},
		{Id: 102, Type: "SWAPARB", Status: "CLOSED"}, // 无结算行
	}
	if err := enrichSettlements(context.Background(), store, pbPositions, []int64{101, 102}); err != nil {
		t.Fatalf("enrichSettlements: %v", err)
	}
	// pos 101：三字段填充。
	if pbPositions[0].SettledRealizedUsd == nil || *pbPositions[0].SettledRealizedUsd != 42.5 {
		t.Errorf("pos 101 settled_realized_usd want 42.5, got %v", pbPositions[0].SettledRealizedUsd)
	}
	if pbPositions[0].SettledNotionalUsd == nil || *pbPositions[0].SettledNotionalUsd != 5000.0 {
		t.Errorf("pos 101 settled_notional_usd want 5000.0, got %v", pbPositions[0].SettledNotionalUsd)
	}
	if pbPositions[0].HoldDays == nil || *pbPositions[0].HoldDays != 3.2 {
		t.Errorf("pos 101 hold_days want 3.2, got %v", pbPositions[0].HoldDays)
	}
	// pos 102：无结算行 → 三字段 nil（缺省）。
	if pbPositions[1].SettledRealizedUsd != nil {
		t.Errorf("pos 102 settled_realized_usd want nil (no settlement), got %v", *pbPositions[1].SettledRealizedUsd)
	}
	if pbPositions[1].HoldDays != nil {
		t.Errorf("pos 102 hold_days want nil (no settlement), got %v", *pbPositions[1].HoldDays)
	}
}

// TestEnrichSettlements_NoSettlement D-106：无结算行 → 全部缺省（不冒充 0）。
func TestEnrichSettlements_NoSettlement(t *testing.T) {
	store := &fakeSettlementQuerier{rows: map[int64]*pgstore.SettlementRow{}}
	pbPositions := []*v1.Position{
		{Id: 201, Type: "SWAPARB", Status: "CLOSED"},
	}
	if err := enrichSettlements(context.Background(), store, pbPositions, []int64{201}); err != nil {
		t.Fatalf("enrichSettlements: %v", err)
	}
	if pbPositions[0].SettledRealizedUsd != nil {
		t.Errorf("no settlement: settled_realized_usd want nil, got %v", *pbPositions[0].SettledRealizedUsd)
	}
	if pbPositions[0].SettledNotionalUsd != nil {
		t.Errorf("no settlement: settled_notional_usd want nil, got %v", *pbPositions[0].SettledNotionalUsd)
	}
	if pbPositions[0].HoldDays != nil {
		t.Errorf("no settlement: hold_days want nil, got %v", *pbPositions[0].HoldDays)
	}
}

// TestEnrichSettlements_EmptyPosIDs D-106：空 posIDs → 不查库，直接返回。
func TestEnrichSettlements_EmptyPosIDs(t *testing.T) {
	store := &fakeSettlementQuerier{rows: map[int64]*pgstore.SettlementRow{}}
	if err := enrichSettlements(context.Background(), store, nil, nil); err != nil {
		t.Fatalf("empty posIDs: %v", err)
	}
}

// TestEnrichSettlements_NilNotional D-106：结算行 notional_usd=NULL（skipped 行）→ settled_notional_usd 缺省。
func TestEnrichSettlements_NilNotional(t *testing.T) {
	store := &fakeSettlementQuerier{rows: map[int64]*pgstore.SettlementRow{
		301: {PositionID: 301, RealizedUsd: 0, NotionalUsd: nil, HoldDays: 0},
	}}
	pbPositions := []*v1.Position{{Id: 301, Type: "SWAPARB", Status: "CLOSED"}}
	if err := enrichSettlements(context.Background(), store, pbPositions, []int64{301}); err != nil {
		t.Fatalf("enrichSettlements: %v", err)
	}
	if pbPositions[0].SettledNotionalUsd != nil {
		t.Errorf("nil notional: settled_notional_usd want nil, got %v", *pbPositions[0].SettledNotionalUsd)
	}
	// realized_usd=0 和 hold_days=0 仍填充（0 是有效值，不是缺省）。
	if pbPositions[0].SettledRealizedUsd == nil {
		t.Error("realized_usd=0 should still be set (0 is valid, not nil)")
	}
}
