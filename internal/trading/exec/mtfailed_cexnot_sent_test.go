// d084_mt_first_test.go —— D-084 锚：XASSET/FH 双腿「MT 先行顺序 taker」。
// (a) MT FAILED → CEX 零触达、终态 FAILED、legs 持久化 MT=FAILED/CEX=PENDING；
// (b) MT FILLED + CEX FILLED → OPEN 且调用序 MT 在前；
// (c) MT FILLED + CEX FAILED → LEGGED（既有 unwind 链）；
// (d) mtFirst=false → 并发路径（两腿 PlaceOrder 都调）；
// (e) SWAPARB（非 XASSET/FH）不受影响仍并发。
package exec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// d084SeqExec 顺序记录执行器（PlaceOrder 调用序入共享 seq；fail=true 返拒绝）。
type d084SeqExec struct {
	venue string
	px    float64
	fail  bool
	mu    *sync.Mutex
	seq   *[]string
}

func (e *d084SeqExec) Key() string { return e.venue }
func (e *d084SeqExec) CloseOrder(context.Context, OrderRef) (Fill, error) {
	return Fill{OrderID: "d084-close-" + e.venue, Price: 100, Qty: 1}, nil
}
func (e *d084SeqExec) TradeModeNow(context.Context, string) (int64, error) { return 4, nil }
func (e *d084SeqExec) PlaceOrder(_ context.Context, o Order) (Fill, error) {
	e.mu.Lock()
	*e.seq = append(*e.seq, e.venue)
	e.mu.Unlock()
	if e.fail {
		return Fill{}, errors.New("fake venue reject")
	}
	return Fill{OrderID: "d084-" + e.venue, Price: e.px, Qty: o.Qty}, nil
}

// d084Pipeline XASSET_CARRY 双腿夹具（d032 T3 同形：opp+allow+margin 三件套缺一不可过闸）。
func d084Pipeline(t *testing.T, mt, cex Executor) (*Pipeline, *pgstore.Store, *sync.Mutex, *[]string) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs, position_settlements, alerts, opportunities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cs := "XAUUSD"
	oppID := "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XAUUSD", CanonicalSell: &cs,
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	reg := NewRegistry()
	reg.Register(mt)
	reg.Register(cex)
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", VenueSymbol: "XAUUSDm", Bid: 99.9, Ask: 100.1, Time: now})
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 99.9, Ask: 100.1, Time: now})
	p := NewPipeline(reg, s, q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1e6}, "demo")
	p.SetContractSizeResolver(&fakeCSResolver{sizes: map[string]float64{
		"3001157:mt5:XAUUSDm": 50, "okx:perp:XAU-USDT-SWAP": 1,
	}})
	p.SetXAssetComboAllow([]XAssetComboAllowSpec{{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSD",
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
		MaxLongQty: 10, MaxShortQty: 100, MaxGrossUSD: 5000,
	}})
	p.SetMarginInputs(nil, nil)
	p.SetMTFirst(true)
	return p, s, nil, nil
}

var d084Legs = []LegSpec{
	{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.4, PriceExpected: 100},
	{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 20, PriceExpected: 100},
}

// (a) MT FAILED → CEX 腿零触达（PlaceOrder 零调用）、position FAILED、
// legs 持久化 MT=FAILED/CEX=PENDING（未发腿审计语义）。
func TestD084_MTFailedCEXNotSent(t *testing.T) {
	mu := &sync.Mutex{}
	seq := &[]string{}
	mt := &d084SeqExec{venue: "3001157:mt5", fail: true, mu: mu, seq: seq}
	cex := &d084SeqExec{venue: "okx:perp", px: 100, mu: mu, seq: seq}
	p, s, _, _ := d084Pipeline(t, mt, cex)
	res, err := p.Execute(context.Background(), "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp", "req-d084-a", "XASSET_CARRY", d084Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seq) != 1 || (*seq)[0] != "3001157:mt5" {
		t.Fatalf("CEX 腿应零触达（seq 仅 MT 一次）, got %v", *seq)
	}
	if res.Status != "FAILED" {
		t.Fatalf("MT 拒单应 FAILED（零成本）, got %s", res.Status)
	}
	legs, lerr := s.ListPositionLegs(context.Background(), res.PositionID)
	if lerr != nil || len(legs) != 2 {
		t.Fatalf("legs: %v n=%d", lerr, len(legs))
	}
	st := map[string]string{}
	for _, l := range legs {
		st[l.Venue] = l.Status
	}
	if st["3001157:mt5"] != "FAILED" || st["okx:perp"] != "PENDING" {
		t.Fatalf("legs 持久化应 MT=FAILED/CEX=PENDING, got %v", st)
	}
}

// (b) MT FILLED + CEX FILLED → OPEN，调用序 MT 在前。
func TestD084_MTFilledThenCEX(t *testing.T) {
	mu := &sync.Mutex{}
	seq := &[]string{}
	p, _, _, _ := d084Pipeline(t,
		&d084SeqExec{venue: "3001157:mt5", px: 100, mu: mu, seq: seq},
		&d084SeqExec{venue: "okx:perp", px: 100, mu: mu, seq: seq})
	res, err := p.Execute(context.Background(), "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp", "req-d084-b", "XASSET_CARRY", d084Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("双腿成交应 OPEN, got %s", res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seq) != 2 || (*seq)[0] != "3001157:mt5" || (*seq)[1] != "okx:perp" {
		t.Fatalf("调用序应 MT→CEX, got %v", *seq)
	}
}

// (c) MT FILLED + CEX FAILED → LEGGED，既有 unwind 链打 legged_unwind（同 D-032 T2 形）。
func TestD084_MTFilledCEXFailedLegged(t *testing.T) {
	mu := &sync.Mutex{}
	seq := &[]string{}
	p, s, _, _ := d084Pipeline(t,
		&d084SeqExec{venue: "3001157:mt5", px: 100, mu: mu, seq: seq},
		&d084SeqExec{venue: "okx:perp", fail: true, mu: mu, seq: seq})
	res, err := p.Execute(context.Background(), "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp", "req-d084-c", "XASSET_CARRY", d084Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mu.Lock()
	got := append([]string{}, (*seq)...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("MT 成交后 CEX 应发单, got %v", got)
	}
	var ek *string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT exit_kind FROM positions WHERE id=$1`, res.PositionID).Scan(&ek); err != nil {
		t.Fatalf("read exit_kind: %v", err)
	}
	if ek == nil || *ek != pgstore.ExitKindLeggedUnwind {
		t.Fatalf("CEX 拒单应 LEGGED→legged_unwind（status=%s）, exit_kind=%v", res.Status, ek)
	}
}

// (d) mtFirst=false → 并发路径（两腿 PlaceOrder 都调，序不保证故按集合断言）。
func TestD084_MTFirstOffConcurrent(t *testing.T) {
	mu := &sync.Mutex{}
	seq := &[]string{}
	p, _, _, _ := d084Pipeline(t,
		&d084SeqExec{venue: "3001157:mt5", px: 100, mu: mu, seq: seq},
		&d084SeqExec{venue: "okx:perp", px: 100, mu: mu, seq: seq})
	p.SetMTFirst(false)
	res, err := p.Execute(context.Background(), "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp", "req-d084-d", "XASSET_CARRY", d084Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("并发双腿成交应 OPEN, got %s", res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seq) != 2 {
		t.Fatalf("并发路径两腿 PlaceOrder 都应调, got %v", *seq)
	}
}

// (e) SWAPARB（非 XASSET/FH 类型）不受 mt-first 影响——两腿并发都调。
func TestD084_SwapArbNotAffected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mu := &sync.Mutex{}
	seq := &[]string{}
	reg := NewRegistry()
	reg.Register(&d084SeqExec{venue: "3001157:mt5", px: 100, mu: mu, seq: seq})
	reg.Register(&d084SeqExec{venue: "okx:perp", px: 100, mu: mu, seq: seq})
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", VenueSymbol: "XAUUSDm", Bid: 99.9, Ask: 100.1, Time: now})
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 99.9, Ask: 100.1, Time: now})
	p := NewPipeline(reg, s, q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1e6}, "demo")
	p.SetContractSizeResolver(&fakeCSResolver{sizes: map[string]float64{
		"3001157:mt5:XAUUSDm": 50, "okx:perp:XAU-USDT-SWAP": 1,
	}})
	p.SetMarginInputs(nil, nil)
	p.SetMTFirst(true) // 开关开，但 SWAPARB 不在适用面
	res, err := p.Execute(ctx, "SWAPARB:XAUUSD:x", "req-d084-e", "SWAPARB", d084Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seq) != 2 {
		t.Fatalf("SWAPARB 应走并发两腿都发, got %v (status=%s)", *seq, res.Status)
	}
}
