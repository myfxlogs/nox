// d346_close_fill_test.go —— D-346：平仓腿成交价/量落库（CLOSE 行 avg_fill_price/filled_qty）。
// 488/489/490 验尸全撞：CLOSE 腿两字段恒 NULL → realized 无法逐腿分解复核。
// T 锚：模拟平仓回报（PlaceOrder fill / 历史回填）→ CLOSE 行两字段落值（红=现恒 NULL）。
package exec

import (
	"context"
	"testing"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// assertCloseFillFields CLOSE FILLED 腿两字段断言（D-346 唯一锚，两路径共用）。
func assertCloseFillFields(t *testing.T, s *pgstore.Store, posID int64, wantPrice, wantQty float64) {
	t.Helper()
	legs, err := s.ListPositionLegsByPhase(context.Background(), posID, "CLOSE")
	if err != nil {
		t.Fatalf("list close legs: %v", err)
	}
	if len(legs) != 1 || legs[0].Status != "FILLED" {
		t.Fatalf("CLOSE FILLED legs=%d, want 1", len(legs))
	}
	l := legs[0]
	if l.AvgFillPrice == nil || *l.AvgFillPrice != wantPrice {
		t.Errorf("CLOSE avg_fill_price = %v, want %v（红=现恒 NULL）", l.AvgFillPrice, wantPrice)
	}
	if l.FilledQty == nil || *l.FilledQty != wantQty {
		t.Errorf("CLOSE filled_qty = %v, want %v（红=现恒 NULL）", l.FilledQty, wantQty)
	}
}

// TestD346_CloseFill_PlaceOrderPath 主路径：broker 仍有腿 → 发平仓单 → fill 回执价/量落库。
func TestD346_CloseFill_PlaceOrderPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"}) // fill: Price=100.0, Qty=下单量
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	// verifier：腿仍在仓（CEX 按 Symbol+Side 匹配；Ticket 异于 OPEN OrderID 防 residual 误报）
	p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
		"okx:perp": {{Symbol: "BTC-USDT-SWAP", Side: "BUY", Ticket: "broker-side-view"}},
	}}

	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSDT:okx:perp::d346', 'PERP_FUNDING', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, OrderID: "T-d346-open", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	if err := p.ClosePosition(ctx, posID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	assertCloseFillFields(t, s, posID, 100.0, 0.001)
}

// TestD346_CloseFill_BackfillPath 回填路径：broker 已无腿 → 权威历史回填 CLOSE 腿同字段。
func TestD346_CloseFill_BackfillPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&fakeNoOrderExecutor{venue: "okx:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{
		"okx:perp:T-d346-bf": {Profit: 5.0, ClosePrice: 101.0, Swap: 0, Commission: 0},
	}})

	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSDT:okx:perp::d346bf', 'PERP_FUNDING', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, OrderID: "T-d346-bf", Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	if err := p.ClosePosition(ctx, posID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	// 回填无独立 fill 回执：filled_qty=OPEN 成交口径（EffectiveQty），avg_fill_price=历史 ClosePrice
	assertCloseFillFields(t, s, posID, 101.0, 0.001)
}

// TestD346_CloseFill_AutoFlattenPath LEGGED 收平路径：反向 MARKET 平仓 fill 价/量落库。
func TestD346_CloseFill_AutoFlattenPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	// BUY 腿开仓成功（okx），SELL 腿开仓失败（binance）→ LEGGED → autoFlatten 反向平 BUY 腿
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp"})
	reg.Register(&fakeFlattenExecutor{venue: "binance:perp", failOnSide: "SELL"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp":     {},
		"binance:perp": {},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:d346fl", "req-d346-fl", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001, PriceExpected: 100000},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 0.001, PriceExpected: 100000},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("期望 CLOSED（auto-flattened），实际 %s", res.Status)
	}
	assertCloseFillFields(t, s, res.PositionID, 100.0, 0.001)
}
