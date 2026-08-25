// d108_p1_test.go —— D-108 P1 A5：SWAPARB 基差止损单测（先红后绿）。
// checkExitCondition SWAPARB/CARRY 分支：basisBps > 4 → 平仓；≤4 不决策；报价缺失不决策。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// TestD108_BasisStopLoss_ExceedsThreshold 基差 > 4 pips → 平仓（return true）。
func TestD108_BasisStopLoss_ExceedsThreshold(t *testing.T) {
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
	pos := seedSwapPosition(t, s)
	// 构造 QuoteBus：buy vA bid=100.0 ask=100.05，sell vB bid=99.90 ask=99.95
	// basis = buyBid - sellAsk = 100.0 - 99.95 = 0.05 → bps = 0.05/100.05*10000 ≈ 5.0 bps > 4
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.05, Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 99.90, Ask: 99.95, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0}}
	shouldClose, reason := e.checkExitCondition(ctx, pos)
	if !shouldClose {
		t.Fatalf("basis > 4: expected shouldClose=true, got false")
	}
	if !strings.Contains(reason, "basis loss limit") {
		t.Fatalf("expected 'basis loss limit' in reason, got: %s", reason)
	}
}

// TestD108_BasisStopLoss_WithinThreshold 基差 ≤ 4 pips → 不平仓（return false）。
func TestD108_BasisStopLoss_WithinThreshold(t *testing.T) {
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
	pos := seedSwapPosition(t, s)
	// basis = buyBid - sellAsk = 100.0 - 99.98 = 0.02 → bps = 0.02/100.05*10000 ≈ 2.0 bps ≤ 4
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 100.0, Ask: 100.05, Time: now})
	qbus.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 99.98, Ask: 99.98, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("basis ≤ 4: expected shouldClose=false, got true")
	}
}

// TestD108_BasisStopLoss_MissingQuote 报价缺失 → 不决策（return false，fail-closed）。
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
	pos := seedSwapPosition(t, s)
	// QuoteBus 空 → 报价缺失 → basis 检查跳过（bok=false），net swap=2>0 不平仓 → false
	qbus := bus.New()
	e := &Engine{store: s, quotes: qbus, cfg: Config{SwapArbExitBps: 0}}
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
	pos := seedSwapPosition(t, s)
	e := &Engine{store: s, quotes: nil, cfg: Config{SwapArbExitBps: 0}}
	shouldClose, _ := e.checkExitCondition(ctx, pos)
	if shouldClose {
		t.Fatal("nil quotes: expected shouldClose=false (fail-closed), got true")
	}
}
