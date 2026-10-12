// d127_p3b_test.go —— D-127 P3b：LEGGED 自动收平单测。
// 场景：纯 CEX 组合，一腿 FILLED 一腿 FAILED → autoFlattenLEGGED 对 FILLED 腿反向平仓。
package exec

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"arb/internal/data/bus"
)

// fakeFlattenExecutor 按 side 返回 FILLED 或 FAILED（模拟一腿成交一腿拒单）。
// failOnSide="SELL" → SELL 返回 error（模拟 binance -1111 拒单），BUY 返回 FILLED。
// placeCount 记录 PlaceOrder 调用次数（验证收平时被调用）。
type fakeFlattenExecutor struct {
	venue      string
	failOnSide string
	placeCount *int32
	feeUSD     float64 // D-422：CLOSE 腿 fee_usd 落库测试用
}

func (e *fakeFlattenExecutor) Key() string { return e.venue }
func (e *fakeFlattenExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	if e.placeCount != nil {
		atomic.AddInt32(e.placeCount, 1)
	}
	if o.Side == e.failOnSide {
		return Fill{}, fmt.Errorf("fake -1111 precision error (side=%s)", o.Side)
	}
	return Fill{OrderID: "fill-" + o.Side, Price: 100.0, Qty: o.Qty, FeeUSD: e.feeUSD}, nil
}
func (e *fakeFlattenExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, fmt.Errorf("cex: use PlaceOrder")
}

// TestD127_AutoFlattenLEGGED_Success 纯 CEX 组合一腿 FILLED 一腿 FAILED →
// autoFlatten 对 FILLED 腿反向平仓 → 全部成功 → status=CLOSED。
func TestD127_AutoFlattenLEGGED_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var buyCount int32
	reg := NewRegistry()
	// BUY 腿成功（okx），SELL 腿失败（binance -1111）
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", failOnSide: "", placeCount: &buyCount})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{}) // D-311 R4：:perp 腿执行器边界换算需要 cs（=1 与旧行为一致）
	// autoFlatten 走 ClosePosition，需要 verifier（残量校验）。
	// 收平后 venue 无残留 → verifier 返回空（不包含已平仓 ticket）。
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:1", "req-d127-1", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	// LEGGED → autoFlatten 成功 → CLOSED
	if res.Status != "CLOSED" {
		t.Fatalf("期望 CLOSED（auto-flattened），实际 %s", res.Status)
	}
	// 验证 okx BUY 腿 PlaceOrder 被调用了 2 次（开仓 + 收平反向 SELL）
	if buyCount < 2 {
		t.Errorf("okx PlaceOrder 调用次数=%d，期望 ≥2（开仓+收平）", buyCount)
	}
	// 验证 position 状态为 CLOSED
	pos, err := s.GetPosition(ctx, res.PositionID)
	if err != nil {
		t.Fatalf("GetPosition: %v", err)
	}
	if pos.Status != "CLOSED" {
		t.Errorf("DB position status=%s，期望 CLOSED", pos.Status)
	}
}

// TestD127_AutoFlattenLEGGED_FlattenFail 收平失败 → 维持 LEGGED（人工兜底）。
func TestD127_AutoFlattenLEGGED_FlattenFail(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	// BUY 腿成功，SELL 腿失败；收平时 BUY 腿反向 SELL 也失败（模拟收平失败）
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", failOnSide: "SELL"}) // 开仓 BUY 成功，收平 SELL 失败
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{}) // D-311 R4：:perp 腿执行器边界换算需要 cs（=1 与旧行为一致）

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:2", "req-d127-2", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	// LEGGED → autoFlatten 失败 → 维持 LEGGED
	if res.Status != "LEGGED" {
		t.Fatalf("期望 LEGGED（auto-flatten 失败），实际 %s", res.Status)
	}
}

// TestD127_AllCEXLegs 辅助函数测试。
func TestD127_AllCEXLegs(t *testing.T) {
	if !allCEXLegs([]LegSpec{{VenueKey: "okx:perp"}, {VenueKey: "binance:perp"}}) {
		t.Error("纯 CEX 腿应返回 true")
	}
	if allCEXLegs([]LegSpec{{VenueKey: "okx:perp"}, {VenueKey: "277842155:mt5"}}) {
		t.Error("含 MT 腿应返回 false")
	}
	if !allCEXLegs([]LegSpec{{VenueKey: "okx:spot"}}) {
		t.Error("单 CEX 腿应返回 true")
	}
}
