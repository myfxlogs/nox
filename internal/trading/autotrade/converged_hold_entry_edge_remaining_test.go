package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// seedConvergedPosition D-424：CROSS_VENUE 双腿持仓（BUY vA / SELL vB）。
func seedConvergedPosition(t *testing.T, s *pgstore.Store) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	pos := &pgstore.Position{
		OpportunityID: "CROSS_VENUE:XAUUSD:vA:vB:1700000000000",
		Type:          "CROSS_VENUE",
		Status:        "OPEN",
	}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: "vA:mt5", Symbol: "XAUUSD",
		Side: "BUY", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg1: %v", err)
	}
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: id, LegNo: 2, Phase: "OPEN", Venue: "vB:mt5", Symbol: "XAUUSD",
		Side: "SELL", Qty: 1.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("create leg2: %v", err)
	}
	return pos
}

// TestD424_ConvergedHold_EntryEdgeRemaining D-424 S1 用例1：
// 入场 edge=+20bps、双腿点差约 1bps → 剩余可捕捉价差 sellBid−buyAsk≈19bps → currentBps≈19 持有（不收敛）。
// 旧腿序（buyBid−sellAsk<0）会 clip 成 0 → 首轮 tick 误判 converged。
func TestD424_ConvergedHold_EntryEdgeRemaining(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := seedConvergedPosition(t, s)
	now := time.Now().UTC()
	qbus := bus.New()
	// 买腿 ask=100.00/bid=99.99（点差1bps）；卖腿 bid=100.19/ask=100.20（点差1bps）
	// 剩余价差 = sellBid − buyAsk = 0.19 → 19bps
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 99.99, Ask: 100.00, Time: now})
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vB", VenueType: "mt5", Bid: 100.19, Ask: 100.20, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{CloseBpsThr: 0.5}}
	bps, ok := e.currentNetBps(pos)
	if !ok {
		t.Fatal("currentNetBps ok=false")
	}
	if bps < 18.5 || bps > 19.5 {
		t.Fatalf("hold case: want currentBps≈19 (remaining spread), got %.4f — 判平=腿序反", bps)
	}
	if bps <= e.cfg.CloseBpsThr {
		t.Fatalf("hold case: currentBps=%.4f <= CloseBpsThr → 误判 converged", bps)
	}
}

// TestD424_ConvergedClose_OverConverged D-424 S1 用例2：
// 卖价跌破买价（过度收敛）→ 剩余价差 ≤0 → clip 0 → currentBps≤0.5 判平。
// 旧腿序此处反而得到正 spread → 判不平（红）。
func TestD424_ConvergedClose_OverConverged(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := seedConvergedPosition(t, s)
	now := time.Now().UTC()
	qbus := bus.New()
	// 买腿 bid=99.99/ask=100.00；卖腿 bid=99.95/ask=99.96 → sellBid−buyAsk=−0.05 → clip→0
	// 旧腿序 buyBid−sellAsk=+0.03 → 3bps → 判不平
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 99.99, Ask: 100.00, Time: now})
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vB", VenueType: "mt5", Bid: 99.95, Ask: 99.96, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{CloseBpsThr: 0.5}}
	bps, ok := e.currentNetBps(pos)
	if !ok {
		t.Fatal("currentNetBps ok=false")
	}
	if bps > e.cfg.CloseBpsThr {
		t.Fatalf("over-converged: want currentBps<=0.5 (converged), got %.4f", bps)
	}
}

// TestD424_ConvergedBoundary_CloseBpsThr D-424 S1 用例3：锚定边界 0.5bps → 判平。
func TestD424_ConvergedBoundary_CloseBpsThr(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := seedConvergedPosition(t, s)
	now := time.Now().UTC()
	qbus := bus.New()
	// sellBid−buyAsk = 0.005 → 0.5bps = CloseBpsThr 边界
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 99.99, Ask: 100.00, Time: now})
	qbus.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vB", VenueType: "mt5", Bid: 100.005, Ask: 100.015, Time: now})

	e := &Engine{store: s, quotes: qbus, cfg: gate.Config{CloseBpsThr: 0.5}}
	bps, ok := e.currentNetBps(pos)
	if !ok {
		t.Fatal("currentNetBps ok=false")
	}
	if bps > e.cfg.CloseBpsThr {
		t.Fatalf("boundary: want currentBps<=0.5, got %.4f", bps)
	}
}
