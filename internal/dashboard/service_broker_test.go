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
	idx, orderIdx := buildPositionIDIndex(ctx, fake)
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
	// S4（D-146）：order_id 索引——MT 模式 order_id==ticket；CEX 模式按 symbol 索引取真实 orderId
	if orderIdx["venue:mt5\x00T1"] != "T1" {
		t.Errorf("orderIdx venue:mt5\\x00T1 want T1, got %q", orderIdx["venue:mt5\x00T1"])
	}
	if orderIdx["venue:mt5\x00AUDJPY"] != "T1" {
		t.Errorf("orderIdx CEX symbol match: venue:mt5\\x00AUDJPY want T1, got %q", orderIdx["venue:mt5\x00AUDJPY"])
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
	idx, orderIdx := buildPositionIDIndex(ctx, fake)
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
	// S4（D-146）：CEX 按 symbol 索引时 order_id 取真实 orderId（≠ ticket=symbol）
	if orderIdx["okx:perp\x00ETH-USDT-SWAP"] != "3869188410747117568" {
		t.Errorf("orderIdx okx:perp by Symbol want 3869188410747117568, got %q", orderIdx["okx:perp\x00ETH-USDT-SWAP"])
	}
	if orderIdx["gate:perp\x00ETH_USDT"] != "11259000692359022" {
		t.Errorf("orderIdx gate:perp by Symbol want 11259000692359022, got %q", orderIdx["gate:perp\x00ETH_USDT"])
	}
}

// TestBuildPositionIDIndex_NilStore D-110 P2：nil store → 空索引（不 panic）。
func TestBuildPositionIDIndex_NilStore(t *testing.T) {
	ctx := context.Background()
	idx, orderIdx := buildPositionIDIndex(ctx, nil)
	if len(idx) != 0 {
		t.Fatalf("nil store: want empty idx, got %d", len(idx))
	}
	if len(orderIdx) != 0 {
		t.Fatalf("nil store: want empty orderIdx, got %d", len(orderIdx))
	}
}

// TestAnnotatePositionID_Regression D-110 P2 A1：抽取后 annotateWithIndex 行为不变。
// S4（D-146）：同时验证 order_id 标注——关联行带出/未关联行空。
func TestAnnotatePositionID_Regression(t *testing.T) {
	posIdx := map[string]int64{
		"venue:mt5\x00T1": 100,
		"venue:mt5\x00T2": 200,
	}
	orderIdx := map[string]string{
		"venue:mt5\x00T1": "T1",
		"venue:mt5\x00T2": "T2",
	}
	bps := []*v1.BrokerPosition{
		{VenueKey: "venue:mt5", Ticket: "T1"},
		{VenueKey: "venue:mt5", Ticket: "T2"},
		{VenueKey: "venue:mt5", Ticket: "ORPHAN"}, // 不在索引中
	}
	annotateWithIndex(bps, posIdx, orderIdx)
	if bps[0].PositionId != 100 {
		t.Errorf("T1 position_id want 100, got %v", bps[0].PositionId)
	}
	if bps[1].PositionId != 200 {
		t.Errorf("T2 position_id want 200, got %v", bps[1].PositionId)
	}
	if bps[2].PositionId != 0 {
		t.Errorf("ORPHAN position_id want 0, got %v", bps[2].PositionId)
	}
	// S4：关联行带出 order_id / 未关联行空
	if bps[0].OrderId != "T1" {
		t.Errorf("T1 order_id want T1, got %q", bps[0].OrderId)
	}
	if bps[2].OrderId != "" {
		t.Errorf("ORPHAN order_id want empty, got %q", bps[2].OrderId)
	}
}

// TestAnnotateWithIndex_IsArbFromPositionID D-311 R7④：PositionId 命中即 IsArb=true——
// CEX 聚合仓无 comment（comment 前缀判定给 false），positionId 是唯一归属信号；
// 未命中行保持 false（MT comment 前缀初值不受影响）。
func TestAnnotateWithIndex_IsArbFromPositionID(t *testing.T) {
	posIdx := map[string]int64{
		"gate:perp\x00CL_USDT": 484,
	}
	bps := []*v1.BrokerPosition{
		{VenueKey: "gate:perp", Ticket: "CL_USDT"},             // CEX 聚合仓，comment 空 → IsArb=false 初值
		{VenueKey: "3001157:mt5", Ticket: "8842", IsArb: true}, // MT comment 前缀已标（模拟既有路径）
		{VenueKey: "okx:perp", Ticket: "TRX-USDT-SWAP"},        // 未命中 → 保持 false
	}
	annotateWithIndex(bps, posIdx, map[string]string{})
	if !bps[0].IsArb {
		t.Error("CEX 仓 positionId 命中 → IsArb want true（旧码恒 false）")
	}
	if !bps[1].IsArb {
		t.Error("MT comment 命中行 IsArb 应保持 true")
	}
	if bps[2].IsArb {
		t.Error("未命中行 IsArb want false")
	}
}
