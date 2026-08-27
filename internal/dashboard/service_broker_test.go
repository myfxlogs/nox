package dashboard

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// fakePositionLegLister 受控 PositionLegLister（buildPositionIDIndex 测试）。
type fakePositionLegLister struct {
	positions []pgstore.Position
	legsByPos map[int64][]pgstore.PositionLeg
}

func (f *fakePositionLegLister) ListPositions(_ context.Context, status string, _, _ int) ([]pgstore.Position, error) {
	if status != "OPEN" {
		return nil, nil
	}
	return f.positions, nil
}
func (f *fakePositionLegLister) ListPositionLegs(_ context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legsByPos[posID], nil
}

// TestBuildPositionIDIndex D-110 P2 A1：抽取后索引构建逻辑正确。
func TestBuildPositionIDIndex(t *testing.T) {
	ctx := context.Background()
	fake := &fakePositionLegLister{
		positions: []pgstore.Position{
			{ID: 100, Status: "OPEN"},
			{ID: 200, Status: "OPEN"},
		},
		legsByPos: map[int64][]pgstore.PositionLeg{
			100: {
				{Phase: "OPEN", Status: "FILLED", Venue: "venue:mt5", OrderID: "T1", Symbol: "AUDJPY"},
				{Phase: "CLOSE", Status: "FILLED", Venue: "venue:mt5", OrderID: "T2"}, // CLOSE 腿不索引
			},
			200: {
				{Phase: "OPEN", Status: "FILLED", Venue: "other:mt4", OrderID: "T3", Symbol: "EURUSD"},
			},
		},
	}
	idx := buildPositionIDIndex(ctx, fake)
	if idx["venue:mt5\x00T1"] != 100 {
		t.Errorf("venue:mt5\\x00T1 want 100, got %v", idx["venue:mt5\x00T1"])
	}
	if idx["other:mt4\x00T3"] != 200 {
		t.Errorf("other:mt4\\x00T3 want 200, got %v", idx["other:mt4\x00T3"])
	}
	if _, ok := idx["venue:mt5\x00T2"]; ok {
		t.Error("CLOSE leg should not be indexed")
	}
	// D-121：CEX 聚合仓位按 venueKey+symbol 索引
	if idx["venue:mt5\x00AUDJPY"] != 100 {
		t.Errorf("D-121 CEX symbol match: venue:mt5\\x00AUDJPY want 100, got %v", idx["venue:mt5\x00AUDJPY"])
	}
	if idx["other:mt4\x00EURUSD"] != 200 {
		t.Errorf("D-121 CEX symbol match: other:mt4\\x00EURUSD want 200, got %v", idx["other:mt4\x00EURUSD"])
	}
}

// TestD121_BuildPositionIDIndex_CEXPerp D-121：CEX perp 聚合仓位按 venueKey+symbol 匹配 positionId。
// CEXPositionQuerier Ticket=Symbol → buildPositionIDIndex 必须按 symbol 索引才能匹配。
func TestD121_BuildPositionIDIndex_CEXPerp(t *testing.T) {
	ctx := context.Background()
	fake := &fakePositionLegLister{
		positions: []pgstore.Position{
			{ID: 360, Status: "OPEN"},
		},
		legsByPos: map[int64][]pgstore.PositionLeg{
			360: {
				{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "3869188410747117568", Symbol: "ETH-USDT-SWAP"},
				{Phase: "OPEN", Status: "FILLED", Venue: "gate:perp", OrderID: "11259000692359022", Symbol: "ETH_USDT"},
			},
		},
	}
	idx := buildPositionIDIndex(ctx, fake)
	// 按 OrderID 匹配（MT 模式）
	if idx["okx:perp\x003869188410747117568"] != 360 {
		t.Errorf("okx:perp by OrderID want 360, got %v", idx["okx:perp\x003869188410747117568"])
	}
	// 按 Symbol 匹配（D-121 CEX 聚合仓位模式）
	if idx["okx:perp\x00ETH-USDT-SWAP"] != 360 {
		t.Errorf("okx:perp by Symbol want 360, got %v", idx["okx:perp\x00ETH-USDT-SWAP"])
	}
	if idx["gate:perp\x00ETH_USDT"] != 360 {
		t.Errorf("gate:perp by Symbol want 360, got %v", idx["gate:perp\x00ETH_USDT"])
	}
}

// TestBuildPositionIDIndex_NilStore D-110 P2：nil store → 空索引（不 panic）。
func TestBuildPositionIDIndex_NilStore(t *testing.T) {
	ctx := context.Background()
	idx := buildPositionIDIndex(ctx, nil)
	if len(idx) != 0 {
		t.Fatalf("nil store: want empty idx, got %d", len(idx))
	}
}

// TestAnnotatePositionID_Regression D-110 P2 A1：抽取后 annotateWithIndex 行为不变。
func TestAnnotatePositionID_Regression(t *testing.T) {
	idx := map[string]int64{
		"venue:mt5\x00T1": 100,
		"venue:mt5\x00T2": 200,
	}
	bps := []*v1.BrokerPosition{
		{VenueKey: "venue:mt5", Ticket: "T1"},
		{VenueKey: "venue:mt5", Ticket: "T2"},
		{VenueKey: "venue:mt5", Ticket: "ORPHAN"}, // 不在索引中
	}
	annotateWithIndex(bps, idx)
	if bps[0].PositionId != 100 {
		t.Errorf("T1 position_id want 100, got %v", bps[0].PositionId)
	}
	if bps[1].PositionId != 200 {
		t.Errorf("T2 position_id want 200, got %v", bps[1].PositionId)
	}
	if bps[2].PositionId != 0 {
		t.Errorf("ORPHAN position_id want 0, got %v", bps[2].PositionId)
	}
}
