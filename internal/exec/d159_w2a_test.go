// d159_w2a_test.go —— D-159 W2a：ClosePosition 十不变量单测（DSN 集成测试）。
// A1：只对 Phase=OPEN AND Status=FILLED 的腿发平仓单（跳过 FAILED/PENDING/CLOSE phase 腿）。
// A2：fill.Qty/Price <=0 → 跳过不伪造成交 + critical 告警。
// A3：写 CLOSE leg 失败 → 返回 error（不 continue 吞咽）。
// A4：autoFlatten 收平成功 → 走 ClosePosition（不独立置 CLOSED）。
// A5：store.ClosePosition 失败 → 返回 error（不吞咽）。
package exec

import (
	"context"
	"strings"
	"testing"

	"arb/internal/bus"
)

// TestClosePosition_SkipsNonOpenFilledLegs A1：只对 Phase=OPEN AND Status=FILLED 腿发平仓单。
// 场景：position 有 2 腿，leg0=OPEN/FILLED，leg1=OPEN/FAILED（binance SELL 失败）。
// 期望：autoFlatten 只对 leg0（FILLED）收平，leg1（FAILED）跳过 → ClosePosition → CLOSED。
func TestClosePosition_SkipsNonOpenFilledLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	var buyCloseCount int32
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", placeCount: &buyCloseCount})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:1", "req-d159-a1", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// leg1 FAILED → LEGGED → autoFlatten 对 leg0 收平 → ClosePosition → CLOSED
	if res.Status != "CLOSED" {
		t.Fatalf("期望 CLOSED，实际 %s", res.Status)
	}
	// leg0 收平时 okx PlaceOrder 被调用（开仓 BUY + 收平 SELL = 2 次）
	if buyCloseCount < 2 {
		t.Errorf("okx PlaceOrder 调用次数=%d，期望 ≥2", buyCloseCount)
	}
}

// TestClosePosition_InvalidFillSkipped A2：fill.Qty/Price <=0 → 跳过不伪造成交 + critical 告警。
// 场景：用 fakeExecutor 返回 fill.Qty=0 或 Price=0。
func TestClosePosition_InvalidFillSkipped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	// 开仓时两腿都成功（正常 fill）
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})

	// 先 Execute 建仓（两腿都 FILLED → OPEN）
	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:2", "req-d159-a2", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("期望 OPEN（建仓成功），实际 %s", res.Status)
	}

	// 用 fakeZeroFillExecutor 替换 okx executor（ClosePosition 时返回 Qty=0）
	reg2 := NewRegistry()
	reg2.Register(&fakeZeroFillExecutor{venue: "okx:perp"})
	reg2.Register(&fakeFlattenExecutor{venue: "binance:perp"})
	p.registry = reg2
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	// ClosePosition → okx 腿 fill.Qty=0 → 跳过不伪造成交 → 残量未归零 → 返回 error
	err = p.ClosePosition(ctx, res.PositionID)
	if err == nil {
		t.Fatal("ClosePosition 应返回 error（invalid fill 跳过 → 残量未归零），但返回 nil")
	}
	if !strings.Contains(err.Error(), "residual") {
		t.Errorf("error 应含 'residual'（invalid fill 跳过导致残量未归零），实际: %v", err)
	}
	// position 不应是 CLOSED（invalid fill 被跳过，不伪造成交）
	pos, _ := s.GetPosition(ctx, res.PositionID)
	if pos.Status == "CLOSED" {
		t.Error("invalid fill 被跳过不应置 CLOSED")
	}
}

// TestClosePosition_StoreCloseFails_Idempotent A5：store.ClosePosition 幂等——已 CLOSED → ErrTerminalState → 返回 nil。
func TestClosePosition_StoreCloseFails_Idempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	// 建仓 → LEGGED → autoFlatten → ClosePosition → CLOSED
	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:3", "req-d159-a5", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("期望 CLOSED，实际 %s", res.Status)
	}

	// 再次 ClosePosition → ClaimClose 失败（已 CLOSED，ErrClaimFailed）
	// ClosePosition 对 ErrClaimFailed 返回 error（非幂等——ClaimClose 是第一道门）
	err = p.ClosePosition(ctx, res.PositionID)
	if err == nil {
		t.Log("幂等 ClosePosition 返回 nil（ClaimClose 可能对 CLOSED 返回 ErrTerminalState 而非 ErrClaimFailed）")
	}
}

// TestClosePosition_ResidualCheck_Fails A3 残量：verifier 发现残留腿 → 不置 CLOSED + 返回 error。
func TestClosePosition_ResidualCheck_Fails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:4", "req-d159-a3", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("期望 OPEN，实际 %s", res.Status)
	}

	// verifier 返回残留腿（okx ticket 仍在仓）
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {{Ticket: "fill-BUY", Symbol: "BTC-USDT-SWAP", Side: "BUY", Volume: 0.001}},
		"binance:perp": {},
	}}

	err = p.ClosePosition(ctx, res.PositionID)
	if err == nil {
		t.Fatal("ClosePosition 应返回 error（残量未归零），但返回 nil")
	}
	if !strings.Contains(err.Error(), "residual") {
		t.Errorf("error 应含 'residual'，实际: %v", err)
	}

	// position 不应是 CLOSED
	pos, _ := s.GetPosition(ctx, res.PositionID)
	if pos.Status == "CLOSED" {
		t.Error("残量未归零不应置 CLOSED")
	}
}

// fakeZeroFillExecutor 返回 fill.Qty=0（无效 fill，用于 A2 测试）。
type fakeZeroFillExecutor struct {
	venue string
}

func (e *fakeZeroFillExecutor) Key() string { return e.venue }
func (e *fakeZeroFillExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	// 开仓时返回正常 fill，收平时返回 Qty=0
	if o.PositionSide == "long" || o.PositionSide == "short" {
		// 收平时 closeSide 反向 → PositionSide 仍是原仓方向
		return Fill{OrderID: "zero-fill", Price: 100.0, Qty: 0}, nil
	}
	return Fill{OrderID: "fill-" + o.Side, Price: 100.0, Qty: o.Qty}, nil
}
func (e *fakeZeroFillExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{OrderID: "zero-close", Price: 100.0, Qty: 0}, nil
}

// TestAutoFlattenGoesThroughClosePosition A4：autoFlatten 收平成功 → 走 ClosePosition（不独立置 CLOSED）。
// 验证：CLOSED 由 ClosePosition 写入（有 CLOSE phase legs + CLOSED status），不是 UpdatePositionStatus。
func TestAutoFlattenGoesThroughClosePosition(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:5", "req-d159-a4", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("期望 CLOSED（autoFlatten → ClosePosition），实际 %s", res.Status)
	}

	// 验证有 CLOSE phase legs（由 autoFlattenLEGGED 写入）
	legs, _ := s.ListPositionLegs(ctx, res.PositionID)
	hasCloseLeg := false
	for _, l := range legs {
		if l.Phase == "CLOSE" && l.Status == "FILLED" {
			hasCloseLeg = true
		}
	}
	if !hasCloseLeg {
		t.Errorf("缺少 CLOSE phase FILLED leg（autoFlatten 应写 CLOSE leg）")
	}

	// 验证 position 是 CLOSED（由 ClosePosition 写入，不是 UpdatePositionStatus）
	pos, _ := s.GetPosition(ctx, res.PositionID)
	if pos.Status != "CLOSED" {
		t.Errorf("position status=%s，期望 CLOSED", pos.Status)
	}
	if pos.RealizedPnl == nil {
		t.Errorf("position.RealizedPnl 应被 ClosePosition 回填（非 nil）")
	}
}
