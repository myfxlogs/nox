// d108_p1_test.go —— D-180 S3 翻正：SWAPARB 基差止损单测（PnL_bps = current − entry < −BasisStopBps）。
// v1 用例（buyBid>sellAsk 触发）语义翻正：亏损=spread 相对 entry 下行>4bps，非旧 A5 的盈利方向绝对值门禁。
// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// seedSwapPositionWithEntry D-180 S3：建一条 SWAPARB 持仓 + 双腿 + expected_entry_spread_bps 基准。
// buyActual/sellActual 为双腿成交价（算 entry spread bps）；entryBps 直接写入 ExpectedEntrySpreadBps。
func seedSwapPositionWithEntry(t *testing.T, s *pgstore.Store, buyActual, sellActual float64) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
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
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, Status: "FILLED", PriceActual: &buyActual,
	}); err != nil {
		t.Fatalf("create leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &sellActual,
	}); err != nil {
		t.Fatalf("create leg2: %v", err)
	}
	// 计算 entry spread bps = (buyActual − sellActual) / buyActual × 10000
	entryBps := (buyActual - sellActual) / buyActual * 10000
	if err := s.UpdateExpectedEntrySpreadBps(ctx, id, entryBps); err != nil {
		t.Fatalf("update entry spread: %v", err)
	}
	pos.ExpectedEntrySpreadBps = &entryBps
	return pos
}

// TestD108_BasisStopLoss_LossExceedsThreshold D-180 S3：亏损构造——current spread 相对 entry 下行 >4bps → 平仓。
// entry=5bps（buyActual=100.05, sellActual=100.0 → entry=5bps），current=0.5bps（spread 收窄 4.5bps > 4）→ 平仓。
func TestD108_BasisStopLoss_LossExceedsThreshold(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	// seed swap facts：net swap = 5 + (-3) = 2 > 0 → 不触发 swap exit，走 basis 检查
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// entry: buyActual=100.05, sellActual=100.0 → entry=(100.05-100.0)/100.05*10000 ≈ 5.0 bps
	pos := seedSwapPositionWithEntry(t, s, 100.05, 100.0)
	// current: buyBid=100.0, sellAsk=100.0 → currentBasis=(100.0-100.0)/refPrice*10000=0 bps
	// PnL = 0 − 5 = −5 bps < −4 → 平仓
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.05, Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 99.95, Ask: 100.0, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0, BasisStopBps: 4.0}}
	shouldClose, reason := e.checkExitCondition(ctx, pos)
	if !shouldClose {
		t.Fatalf("loss > 4bps: expected shouldClose=true, got false")
	}
	if !strings.Contains(reason, "basis loss") {
		t.Fatalf("expected 'basis loss' in reason, got: %s", reason)
	}
}

// TestD108_BasisStopLoss_ProfitNotTrigger D-180 S3：盈利构造——current spread 相对 entry 上行 → 不平仓。
// entry=5bps，current=8bps（spread 扩大 3bps，PnL=+3 > −4）→ 不平仓。
func TestD108_BasisStopLoss_ProfitNotTrigger(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// entry: buyActual=100.05, sellActual=100.0 → entry ≈ 5.0 bps
	pos := seedSwapPositionWithEntry(t, s, 100.05, 100.0)
	// current: buyBid=100.08, sellAsk=100.0 → currentBasis=(100.08-100.0)/100.05*10000 ≈ 8.0 bps
	// PnL = 8 − 5 = +3 bps > −4 → 不平仓
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.08, Ask: 100.05, Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 99.95, Ask: 100.0, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0, BasisStopBps: 4.0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("profit (PnL=+3 > -4): expected shouldClose=false, got true")
	}
}

// TestD108_BasisStopLoss_EntryNullFailClosed D-180 S3：基准缺失（ExpectedEntrySpreadBps nil）→ 不决策（fail-closed）。
func TestD108_BasisStopLoss_EntryNullFailClosed(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	// 用 seedSwapPosition（无 entry 基准，ExpectedEntrySpreadBps nil）
	pos := seedSwapPosition(t, s)
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.05, Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 99.90, Ask: 99.95, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0, BasisStopBps: 4.0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("entry NULL: expected shouldClose=false (fail-closed), got true")
	}
}

// TestD108_BasisStopLoss_MissingQuote 报价缺失 → 不决策（fail-closed）。
func TestD108_BasisStopLoss_MissingQuote(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPositionWithEntry(t, s, 100.05, 100.0)
	// QuoteBus 空 → 报价缺失 → basis 检查跳过（bok=false），net swap=2>0 不平仓 → false
	qbus := bus.New()
	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0, BasisStopBps: 4.0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("missing quote: expected shouldClose=false (fail-closed), got true")
	}
}

// TestD108_BasisStopLoss_NilQuotes quotes=nil → basis 检查跳过（fail-closed 不决策）。
func TestD108_BasisStopLoss_NilQuotes(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts, positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	pos := seedSwapPositionWithEntry(t, s, 100.05, 100.0)
	e := &Engine{store: s, quotes: nil, cfg: Config{SwapArbExitBps: 0, BasisStopBps: 4.0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("nil quotes: expected shouldClose=false (fail-closed), got true")
	}
}

// TestD180_BasisStopLossCheck_PureFunction 纯函数测试——basisStopLossCheck 各边界。
func TestD180_BasisStopLossCheck_PureFunction(t *testing.T) {
	entry := 5.0
	// 亏损 >4bps → 平仓
	if shouldClose, _ := basisStopLossCheck(0.0, true, &entry, 4.0); !shouldClose {
		t.Errorf("PnL=-5 < -4: want shouldClose=true")
	}
	// 亏损 =4bps（边界，PnL=-4 不 < -4）→ 不平仓
	if shouldClose, _ := basisStopLossCheck(1.0, true, &entry, 4.0); shouldClose {
		t.Errorf("PnL=-4 (boundary): want shouldClose=false")
	}
	// 盈利 → 不平仓
	if shouldClose, _ := basisStopLossCheck(8.0, true, &entry, 4.0); shouldClose {
		t.Errorf("PnL=+3: want shouldClose=false")
	}
	// 基准 nil → fail-closed
	if shouldClose, _ := basisStopLossCheck(0.0, true, nil, 4.0); shouldClose {
		t.Errorf("entry nil: want shouldClose=false (fail-closed)")
	}
	// 报价缺失 → fail-closed
	if shouldClose, _ := basisStopLossCheck(0.0, false, &entry, 4.0); shouldClose {
		t.Errorf("currentOk=false: want shouldClose=false (fail-closed)")
	}
	// BasisStopBps<=0 → 不校验
	if shouldClose, _ := basisStopLossCheck(0.0, true, &entry, 0); shouldClose {
		t.Errorf("BasisStopBps=0: want shouldClose=false (disabled)")
	}
}

// TestD180_ComputeEntrySpreadFromLegs 纯函数测试——双腿成交价算 entry spread。
func TestD180_ComputeEntrySpreadFromLegs(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	buyActual, sellActual := 100.05, 100.0
	pos := &pgstore.Position{OpportunityID: "SWAPARB:AUDJPY:vA:vB:1", Type: "SWAPARB", Status: "OPEN"}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, Status: "FILLED", PriceActual: &buyActual,
	}); err != nil {
		t.Fatalf("leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &sellActual,
	}); err != nil {
		t.Fatalf("leg2: %v", err)
	}
	entryBps, ok := computeEntrySpreadFromLegs(ctx, s, id)
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := (100.05 - 100.0) / 100.05 * 10000
	if absDiffBps(entryBps, want) > 0.01 {
		t.Errorf("entryBps: want %.4f, got %.4f", want, entryBps)
	}
}

// TestD180_BackfillEntrySpread 存量回填——活跃仓无基准 → 回填；已有基准 → 跳过。
func TestD180_BackfillEntrySpread(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	buyActual, sellActual := 100.05, 100.0
	pos := &pgstore.Position{OpportunityID: "SWAPARB:AUDJPY:vA:vB:1", Type: "SWAPARB", Status: "OPEN"}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "AUDJPY",
		Side: "BUY", Qty: 1.0, Status: "FILLED", PriceActual: &buyActual,
	}); err != nil {
		t.Fatalf("leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "AUDJPY",
		Side: "SELL", Qty: 1.0, Status: "FILLED", PriceActual: &sellActual,
	}); err != nil {
		t.Fatalf("leg2: %v", err)
	}
	e := &Engine{store: s}
	e.backfillEntrySpread(ctx)
	// 验证回填后 ExpectedEntrySpreadBps 非 nil
	active, _ := s.ListActivePositions(ctx)
	if len(active) != 1 {
		t.Fatalf("want 1 active position, got %d", len(active))
	}
	if active[0].ExpectedEntrySpreadBps == nil {
		t.Fatal("ExpectedEntrySpreadBps: want non-nil after backfill, got nil")
	}
	want := (100.05 - 100.0) / 100.05 * 10000
	if absDiffBps(*active[0].ExpectedEntrySpreadBps, want) > 0.01 {
		t.Errorf("entryBps: want %.4f, got %.4f", want, *active[0].ExpectedEntrySpreadBps)
	}
	// 再次回填 → 幂等不覆写（值不变）
	e.backfillEntrySpread(ctx)
	active, _ = s.ListActivePositions(ctx)
	if absDiffBps(*active[0].ExpectedEntrySpreadBps, want) > 0.01 {
		t.Errorf("2nd backfill idempotent: want %.4f, got %.4f", want, *active[0].ExpectedEntrySpreadBps)
	}
}
