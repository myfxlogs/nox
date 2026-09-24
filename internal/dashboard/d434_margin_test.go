// d434_margin_test.go —— D-434：CEX 腿名义额单位错位修复（T1-T5/T7 锚）。
// P1：CEX filled_qty=基础币被当合约张数 × ct_val → ~1000x 低估（pos502 实证 $4.36 vs 真值 $4362）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// d434CS stub：CEX 返 ct_val、MT 返合约规格（仅回落路径/MT 路径消费）。
func d434CS(venueKey, symbol string) (float64, error) {
	switch {
	case venueKey == "okx:perp":
		return 0.001, nil // XAU-USDT-SWAP ct_val=0.001oz（pos502 实参）
	case isMTVenue(venueKey):
		return 100, nil // MT XAUUSD cs=100oz
	default:
		return 1, nil // CEX spot/perp 默认 cs=1（binance 实态）
	}
}

// TestD434_Evaluate_CEX_FilledNotional T5 e2e（主锚）：okx perp 腿 filled=1.016 →
// daily_vol_usd 真值量级（非 0.04 级）。红=旧码 dailyVol≈$0.2（<1）。
func TestD434_Evaluate_CEX_FilledNotional(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.EnsureMonthPartition(ctx, now.Year(), int(now.Month())); err != nil {
		t.Fatalf("EnsureMonthPartition: %v", err)
	}
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now.Add(-3 * time.Hour), Venue: "okx", VenueType: "perp", Symbol: "XAUUSDT", Bid: 100, Ask: 100.1},
		{Ts: now.Add(-30 * time.Minute), Venue: "okx", VenueType: "perp", Symbol: "XAUUSDT", Bid: 110, Ask: 110.1},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
	// pos502 实参腿：qty=1016 张 / filled=1.016 基础币 / 价 4297
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "XASSET_CARRY:XAUUSD:okx:perp:3001157:mt5", Type: "XASSET_CARRY",
		Status: "OPEN", Environment: "live",
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	qty, filled, price := 1016.0, 1.016, 4297.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
		Side: "SELL", Qty: qty, Status: "FILLED", FilledQty: &filled, AvgFillPrice: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	g := NewMarginGuard(s, bus.New(), d434CS, time.Minute, time.Minute)
	sn := &pgstore.VenueAccountSnapshot{
		Venue: "okx", VenueType: "unified", EquityRaw: 10000, BufferMultiple: f64p(5),
		Currency: "USD", FetchedAt: time.Now(),
	}
	if err := g.evaluate(ctx, sn); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	r, ok, err := s.GetVenueMarginStatus(ctx, "okx", "unified")
	if err != nil || !ok {
		t.Fatalf("GetVenueMarginStatus: ok=%v err=%v", ok, err)
	}
	if r.Tier == "data_stale" {
		t.Fatal("tier=data_stale（tick 键域应已由 D-430 修复）")
	}
	// 真值量级：notional≈1.016×4297=$4366，dailyVol≈4366×0.0476≈$208（旧码≈$0.21）
	if r.DailyVolUSD == nil || *r.DailyVolUSD <= 1 {
		t.Fatalf("DailyVolUSD=%v want >$1（红=CEX 名义额 ~1000x 低估回归）", r.DailyVolUSD)
	}
	if *r.DailyVolUSD < 100 || *r.DailyVolUSD > 300 {
		t.Fatalf("DailyVolUSD=%.2f want ≈$208 量级", *r.DailyVolUSD)
	}
}

// TestD434_MarginFloorEval_CEX T7 e2e：marginFloorEval okx 有腿+buffer →
// 隐含保证金率建立在修正名义额上（required 量级断言，防过度拦回归）。
// 口径：eq=1515.9 buffer=86.87（mmr≈17.45）→ avail≈1498.45；腿名义≈$4366 → 隐含率≈0.4%。
// addNotional=$4366 → required≈$17.45 放行；addNotional=$400000 → required≈$1600 拦截。
// 红：旧码 sumNotional≈$4.37 → 隐含率≈400% → 首个调用即拦（ok=false）。
func TestD434_MarginFloorEval_CEX(t *testing.T) {
	s := d405Store(t)
	ctx := context.Background()
	posID, err := s.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "XASSET_CARRY:XAUUSD:okx:perp:3001157:mt5", Type: "XASSET_CARRY",
		Status: "OPEN", Environment: "live",
	})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	qty, filled, price := 1016.0, 1.016, 4297.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
		Side: "SELL", Qty: qty, Status: "FILLED", FilledQty: &filled, AvgFillPrice: &price,
	}); err != nil {
		t.Fatalf("create leg: %v", err)
	}
	buf := 86.87
	if err := s.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{{
		Venue: "okx", VenueType: "unified", EquityRaw: 1515.9, BufferMultiple: &buf,
		Currency: "USD", FetchedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("insert snapshot: %v", err)
	}
	svc := &Service{
		store: s, qbus: bus.New(),
		cexContractSize: func(venue, symbol string) (float64, bool) { return 0.001, true },
	}
	ok, known, detail := svc.marginFloorEval(ctx, "okx:perp", 4366)
	if !ok || !known {
		t.Fatalf("addNotional=$4366: ok=%v known=%v detail=%s（红=名义低估→隐含率虚高过度拦）", ok, known, detail)
	}
	ok, known, detail = svc.marginFloorEval(ctx, "okx:perp", 400000)
	if ok || !known {
		t.Fatalf("addNotional=$400000: ok=%v known=%v detail=%s（required≈$1600>avail$1498 应拦）", ok, known, detail)
	}
}
