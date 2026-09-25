// d480_closenet_test.go D-480：平仓净估验收锚——出口价浮动/退出成本/缺报价 NULL/超窗快照近似。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// d480Svc CEX 腿测试面：qbus+commissionFor+oppContractSize+freshTTL 注入（store=nil 不触）。
func d480Svc(qb *bus.QuoteBus) *Service {
	return &Service{
		qbus:            qb,
		freshTTL:        30 * time.Second,
		commissionFor:   func(vk string) float64 { return 5.0 }, // taker 5bps
		xassetCexFeeBps: 4.0,
		oppContractSize: func(string, string) (float64, bool) { return 1, false }, // cs=1
	}
}

// TestD480_CloseNowNet_Mixed：MT+CEX 混合仓——CEX 腿按出口价重算浮动−退出费；MT 腿快照 profit 直通。
func TestD480_CloseNowNet_Mixed(t *testing.T) {
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", Bid: 2399, Ask: 2401, Time: now})
	s := d480Svc(qb)
	pb := &v1.Position{Id: 502, OpportunityId: "XASSET:XAUUSDT:3001157:binance", Status: "OPEN"}
	px := 2400.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XAUUSDm", Side: "BUY", Status: "FILLED", Qty: 0.01, PriceActual: &px, Ts: now},
		{Phase: "OPEN", Venue: "binance:perp", Symbol: "XAUUSDT", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: &px, Ts: now},
	}
	snap := []*v1.BrokerPosition{
		{VenueKey: "3001157:mt5", Symbol: "XAUUSDm", Pnl: 3.0, Swap: 0.5, PositionId: 502},
		{VenueKey: "binance:perp", Symbol: "XAUUSDT", Pnl: 1.0, PositionId: 502},
	}
	// CEX SELL 腿：买回价 ask=2401 → 浮动 −(2401−2400)×1=−1；退出费=2400×1×5bps/1e4=1.2
	// net = 快照浮动(3+1)+swap(0.5)+comm(0) = 4.5；close = 3+(−1)+0.5−1.2 = 1.3
	net, cost, ok := s.closeNowNet(context.Background(), pb, legs, snap, 0.5, 0, now)
	if !ok {
		t.Fatal("closeNowNet should produce value")
	}
	if math.Abs(net-1.3) > 1e-9 {
		t.Errorf("close_now_net=%.4f want 1.3（mt 3 + cex −1 + swap 0.5 − fee 1.2）", net)
	}
	if math.Abs(cost-1.2) > 1e-9 {
		t.Errorf("exit_cost=%.4f want 1.2", cost)
	}
	// 差值断言：close_now_net 与账面 net 差=出口价差(1.0−(−1)=2)+退出费1.2=3.2
	if math.Abs(4.5-net-3.2) > 1e-9 {
		t.Errorf("net−close 差值=%.4f want 3.2", 4.5-net)
	}
}

// TestD480_CloseNowNet_QuoteMissing_Null：CEX 腿报价缺→整列 NULL（fail-closed 不猜）。
func TestD480_CloseNowNet_QuoteMissing_Null(t *testing.T) {
	s := d480Svc(bus.New()) // 无报价
	pb := &v1.Position{Id: 502, OpportunityId: "XASSET:XAUUSDT:binance:okx", Status: "OPEN"}
	px := 2400.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:perp", Symbol: "XAUUSDT", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: &px, Ts: time.Now()},
	}
	if _, _, ok := s.closeNowNet(context.Background(), pb, legs, nil, 0.5, 0, time.Now()); ok {
		t.Fatal("报价缺→应 NULL")
	}
}

// TestD480_CloseNowNet_StaleQuote_SnapFallback：报价超窗→该腿快照 profit 近似（值照出）。
func TestD480_CloseNowNet_StaleQuote_SnapFallback(t *testing.T) {
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", Bid: 2399, Ask: 2401, Time: now.Add(-2 * time.Minute)})
	s := d480Svc(qb)
	pb := &v1.Position{Id: 502, OpportunityId: "XASSET:XAUUSDT:binance:okx", Status: "OPEN"}
	px := 2400.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "binance:perp", Symbol: "XAUUSDT", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: &px, Ts: now},
	}
	snap := []*v1.BrokerPosition{{VenueKey: "binance:perp", Symbol: "XAUUSDT", Pnl: 2.0, PositionId: 502}}
	// 超窗→快照 2.0 + swap 0.5 − 退出费 1.2 = 1.3
	net, cost, ok := s.closeNowNet(context.Background(), pb, legs, snap, 0.5, 0, now)
	if !ok {
		t.Fatal("超窗快照近似应照出值")
	}
	if math.Abs(net-1.3) > 1e-9 || math.Abs(cost-1.2) > 1e-9 {
		t.Errorf("net=%.4f cost=%.4f want 1.3/1.2", net, cost)
	}
}

// TestD480_CloseNowNet_MTOnly_NoQbus：纯 MT 仓无 CEX 腿→qbus 缺省也照出（快照权威）。
func TestD480_CloseNowNet_MTOnly_NoQbus(t *testing.T) {
	s := &Service{} // qbus/store 全 nil——无 CEX 腿不触
	pb := &v1.Position{Id: 264, OpportunityId: "SWAPARB:AUDJPY:vA:vB", Status: "OPEN"}
	px := 113.5
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "52993526:mt5", Symbol: "AUDJPYm", Side: "BUY", Status: "FILLED", Qty: 1, PriceActual: &px, Ts: time.Now()},
	}
	snap := []*v1.BrokerPosition{{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Pnl: -40, Swap: 4.99, Commission: -1, PositionId: 264}}
	// = −40 + 4.99 + (−1) − 0 = −36.01
	net, cost, ok := s.closeNowNet(context.Background(), pb, legs, snap, 4.99, -1, time.Now())
	if !ok || cost != 0 {
		t.Fatalf("MT-only ok=%v cost=%v want ok/0", ok, cost)
	}
	if math.Abs(net-(-36.01)) > 1e-9 {
		t.Errorf("net=%.4f want −36.01", net)
	}
}

// TestD480_Enrich_RefreshColumn：enrichOne 端到端——matched 仓 close_now_net 落列+门控外不落。
func TestD480_Enrich_RefreshColumn(t *testing.T) {
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Pnl: -40, Swap: 4.99, PositionId: 264},
	})
	s := &Service{bpm: bpm} // qbus nil+纯 MT 腿→值照出
	pb := &v1.Position{
		Id: 264, OpportunityId: "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5",
		Type: "SWAPARB", Status: "OPEN", OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "52993526:mt5", Symbol: "AUDJPYm", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(113.517), Ts: time.Now().AddDate(0, 0, -3)},
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "AUDJPY.", Status: "FILLED", Qty: 1, PriceActual: ptrFloat(113.503), Ts: time.Now().AddDate(0, 0, -3)},
	}
	s.enrichPositions(context.Background(), []*v1.Position{pb}, map[int64][]pgstore.PositionLeg{264: legs})
	if pb.CloseNowNetUsd == nil {
		t.Fatal("matched 仓 close_now_net 应落列")
	}
	// floating=−40（277842155 无快照腿），swap=4.99，comm=0 → close=−35.01
	if math.Abs(*pb.CloseNowNetUsd-(-35.01)) > 0.001 {
		t.Errorf("close_now_net=%.4f want −35.01", *pb.CloseNowNetUsd)
	}
	if pb.CloseExitCostsUsd == nil || *pb.CloseExitCostsUsd != 0 {
		t.Errorf("exit_costs=%v want 0（MT 腿点差内嵌）", pb.CloseExitCostsUsd)
	}
	// 门控外：无匹配仓不落列
	pb2 := &v1.Position{Id: 999, OpportunityId: "SWAPARB:AUDJPY:x:y", Type: "SWAPARB", Status: "OPEN"}
	s.enrichPositions(context.Background(), []*v1.Position{pb2}, map[int64][]pgstore.PositionLeg{999: legs})
	if pb2.CloseNowNetUsd != nil {
		t.Error("未匹配仓 close_now_net 应缺省")
	}
}

// TestD480_CloseNowNet_NativeSymbolKey R1：oppID canonical=XAU 与采集键 XAUUSDT 错位——
// 腿原生 symbol（okx XAU-USDT-SWAP）去横线归一后命中报价（采集器产出权威键）。
func TestD480_CloseNowNet_NativeSymbolKey(t *testing.T) {
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 2399, Ask: 2401, Time: now}) // 采集器键=去横线原生
	s := d480Svc(qb)
	pb := &v1.Position{Id: 502, OpportunityId: "XASSET:XAU:3001157:okx", Status: "OPEN"} // canonical=XAU≠XAUUSDT
	px := 2400.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XAUUSDm", Side: "BUY", Status: "FILLED", Qty: 0.01, PriceActual: &px, Ts: now},
		{Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP", Side: "SELL", Status: "FILLED", Qty: 1, PriceActual: &px, Ts: now},
	}
	snap := []*v1.BrokerPosition{{VenueKey: "3001157:mt5", Symbol: "XAUUSDm", Pnl: 3.0, PositionId: 502}}
	net, cost, ok := s.closeNowNet(context.Background(), pb, legs, snap, 0, 0, now)
	if !ok {
		t.Fatal("canonical 错位场景应命中原生 symbol 键（XAU-USDT-SWAP→XAUUSDT）")
	}
	// cex SELL: −(2401−2400)=−1；fee=2400×5bps=1.2；mt +3 → 3−1−1.2=0.8
	if math.Abs(net-0.8) > 1e-9 || math.Abs(cost-1.2) > 1e-9 {
		t.Errorf("net=%.4f cost=%.4f want 0.8/1.2", net, cost)
	}
}
