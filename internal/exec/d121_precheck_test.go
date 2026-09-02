package exec

import (
	"context"
	"errors"
	"testing"

	"arb/internal/bus"
)

// fakePrecheckExecutor 实现 Executor + Prechecker（D-121 预检测试）。
type fakePrecheckExecutor struct {
	venue       string
	fill        Fill
	precheckErr error // 预设预检返回错误（nil=通过）
	placed      bool  // 下单是否被调用（预检失败应=false）
}

func (e *fakePrecheckExecutor) Key() string { return e.venue }
func (e *fakePrecheckExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.placed = true
	return e.fill, nil
}
func (e *fakePrecheckExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return e.fill, nil
}
func (e *fakePrecheckExecutor) Precheck(ctx context.Context, o Order) error {
	return e.precheckErr
}

// TestPipeline_PrecheckFailsRejects 预检失败 → 不建 position 不下单（防 LEGGED）。
func TestPipeline_PrecheckFailsRejects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	reg := NewRegistry()
	gate := &fakePrecheckExecutor{
		venue:       "gate:perp",
		fill:        Fill{OrderID: "g1", Price: 100, Qty: 1},
		precheckErr: errors.New("gate precheck TRX_USDT: http 404: {\"label\":\"CONTRACT_NOT_FOUND\"}"),
	}
	binance := &fakePrecheckExecutor{
		venue: "binance:perp",
		fill:  Fill{OrderID: "b1", Price: 100, Qty: 1},
	}
	reg.Register(gate)
	reg.Register(binance)

	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")

	_, err := p.Execute(ctx, "opp-trx-1", "req-1", "PERP_FUNDING", []LegSpec{
		{VenueKey: "gate:perp", VenueSymbol: "TRX_USDT", Side: "BUY", Qty: 1},
		{VenueKey: "binance:perp", VenueSymbol: "TRXUSDT", Side: "SELL", Qty: 1},
	})
	if err == nil {
		t.Fatal("预检失败应返回 error")
	}
	// 预检失败 → binance 不应下单
	if binance.placed {
		t.Fatal("预检失败 → binance PlaceOrder 不应被调用（防 LEGGED）")
	}
	// 预检失败 → 不建 position
	positions, _ := s.ListPositions(ctx, "", 10, 0)
	if len(positions) > 0 {
		t.Fatalf("预检失败 → 不应建 position，实际 %d 条", len(positions))
	}
}

// TestPipeline_PrecheckPassesExecutes 预检通过 → 正常下单建 position。
func TestPipeline_PrecheckPassesExecutes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	reg := NewRegistry()
	gate := &fakePrecheckExecutor{
		venue: "gate:perp",
		fill:  Fill{OrderID: "g1", Price: 100, Qty: 1},
	}
	binance := &fakePrecheckExecutor{
		venue: "binance:perp",
		fill:  Fill{OrderID: "b1", Price: 100, Qty: 1},
	}
	reg.Register(gate)
	reg.Register(binance)

	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")

	result, err := p.Execute(ctx, "opp-btc-1", "req-1", "PERP_FUNDING", []LegSpec{
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "BUY", Qty: 1},
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 1},
	})
	if err != nil {
		t.Fatalf("预检通过应无 error，got %v", err)
	}
	if result.Status != "OPEN" {
		t.Fatalf("双腿成交应 OPEN，got %s", result.Status)
	}
	if !gate.placed || !binance.placed {
		t.Fatal("预检通过 → 两腿都应下单")
	}
}
