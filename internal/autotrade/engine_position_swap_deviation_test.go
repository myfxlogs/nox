// engine_position_swap_deviation_test.go —— D-165 S2：recalcNetSwap broker 实际 swap 对拍单测。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// seedSwapDeviationPosition 构造 OPEN 持仓 + legs（带 price_actual）+ facts + swap snapshots。
// factsLong/factsShort：facts 规格值（bps）；buyActualUSD/sellActualUSD：broker 实际 swap 日增量（USD）。
func seedSwapDeviationPosition(t *testing.T, s *pgstore.Store, factsLong, factsShort, buyActualUSD, sellActualUSD float64) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, position_swap_snapshots CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, position_swap_snapshots CASCADE`)
	})
	now := time.Now().UTC()
	// facts
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: factsLong, Unit: fact.UnitBpsDaily, Ts: now, Src: "test"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: factsShort, Unit: fact.UnitBpsDaily, Ts: now, Src: "test"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// position + legs（price_actual=100，qty=1，notional=100）
	pos := &pgstore.Position{
		OpportunityID: "SWAPARB:AUDJPY:vA:vB:1700000000000",
		Type:          "SWAPARB",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id
	price := 100.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, PriceExpected: price, PriceActual: &price, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, PriceExpected: price, PriceActual: &price, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg2: %v", err)
	}
	// swap snapshots：昨日 0 + 今日 actualUSD（DailySwapAccrued = today - yesterday = actualUSD）
	// D-189 P0-1：快照 venue 必须是完整 venueKey（与生产写入口径一致，legs.Venue 同口径）。
	yesterday := now.AddDate(0, 0, -1)
	if err := s.InsertSwapSnapshot(ctx, id, "vA:mt5", 0, yesterday); err != nil {
		t.Fatalf("insert snap vA yesterday: %v", err)
	}
	if err := s.InsertSwapSnapshot(ctx, id, "vA:mt5", buyActualUSD, now); err != nil {
		t.Fatalf("insert snap vA today: %v", err)
	}
	if err := s.InsertSwapSnapshot(ctx, id, "vB:mt5", 0, yesterday); err != nil {
		t.Fatalf("insert snap vB yesterday: %v", err)
	}
	if err := s.InsertSwapSnapshot(ctx, id, "vB:mt5", sellActualUSD, now); err != nil {
		t.Fatalf("insert snap vB today: %v", err)
	}
	return pos
}

// TestRecalcNetSwap_DeviationHigh_UseBrokerActual D-165 S2：偏差 > 50% → 用 broker 实际值。
// D-190 返修 R1：AUDJPY 为 JPY 对 → 需 e.quotes 注入 USDJPY 报价让 PerLot ok。
// CS=1（无 resolver 注入→回退），price=100, USDJPY=150 → usdPerLot=0.667, notional=0.667。
// actual: buy=-2.21, sell=0.5, net=-1.71。actualBps=-1.71/0.667×10000≈-25650。
// facts=0.5。偏差 > 50% → 用 actual（负）。
func TestRecalcNetSwap_DeviationHigh_UseBrokerActual(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{store: s, quotes: newJPYQuotes(), cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	if got >= 0 {
		t.Errorf("high deviation: got %.4f, want negative (broker actual)", got)
	}
}

// TestRecalcNetSwap_DeviationLow_UseFacts D-165 S2：偏差 < 50% → 用 facts 规格值（原行为）。
// facts=0.5 bps，broker actual: buy=0.004 USD, sell=0.001 USD, net=0.005, actualBps=0.05。
// 偏差 |0.5-0.05|/max(0.5,0.05,0.01)*100 = 0.45/0.5*100 = 90% > 50% → 仍用 actual。
// 改用更接近的值：facts=0.5, actual=0.48 → 偏差 4% < 50% → 用 facts。
func TestRecalcNetSwap_DeviationLow_UseFacts(t *testing.T) {
	s := testAccumStore(t)
	// actualBps=0.48 → netUSD=0.0048, buy=0.0024, sell=0.0024
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, 0.0024, 0.0024)
	e := &Engine{store: s, cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	want := 0.5 // facts 规格值
	if got < want-0.01 || got > want+0.01 {
		t.Errorf("low deviation: got %.4f, want ~%.4f (facts)", got, want)
	}
}

// TestRecalcNetSwap_NoSnapshot_UseFacts D-165 S2：无 broker 快照 → 用 facts 规格值（回退）。
func TestRecalcNetSwap_NoSnapshot_UseFacts(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, position_swap_snapshots CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs, position_swap_snapshots CASCADE`)
	})
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 0.5, Unit: fact.UnitBpsDaily, Ts: now, Src: "test"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: 0.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "test"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPosition(t, s) // 无 swap snapshots
	e := &Engine{store: s, cfg: Config{SwapDeviationExitPct: 50.0}}
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	want := 0.5 // facts 规格值
	if got != want {
		t.Errorf("no snapshot: got %.4f, want %.4f (facts fallback)", got, want)
	}
}

// TestRecalcNetSwap_DeviationDisabled_UseFacts D-165 S2 反证：SwapDeviationExitPct=0（禁用）→ 高偏差也用 facts。
func TestRecalcNetSwap_DeviationDisabled_UseFacts(t *testing.T) {
	s := testAccumStore(t)
	pos := seedSwapDeviationPosition(t, s, 0.5, 0.0, -2.21, 0.5)
	e := &Engine{store: s, cfg: Config{SwapDeviationExitPct: 0}} // 禁用
	got, ok := e.recalcNetSwap(context.Background(), pos)
	if !ok {
		t.Fatalf("want ok=true, got false")
	}
	want := 0.5 // facts 规格值
	if got != want {
		t.Errorf("disabled: got %.4f, want %.4f (facts, no deviation check)", got, want)
	}
}
