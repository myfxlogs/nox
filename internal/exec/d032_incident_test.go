// d032_incident_test.go —— D-032 R1 锚（真 Execute 写路径）：
// T1 全腿零成交 FAILED → 不打 legged_unwind，事故闸放行（#503/#504 形态回归）；
// T2 一腿成交一腿失败 → LEGGED → legged_unwind，事故闸拦截；
// T3 XASSET_CARRY OPEN → positions.notional_usd=Σ腿名义（MT lots×CS、CEX base 直接）。
package exec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

var d032Legs = []LegSpec{
	{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceExpected: 100},
	{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 100},
}

func d032Pipeline(t *testing.T, mtLeg, cexLeg Executor) (*Pipeline, *pgstore.Store) {
	t.Helper()
	s := testStore(t)
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE positions, position_legs, position_settlements, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(mtLeg)
	reg.Register(cexLeg)
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", VenueSymbol: "XAUUSDm", Bid: 99.9, Ask: 100.1, Time: now})
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 99.9, Ask: 100.1, Time: now})
	p := NewPipeline(reg, s, q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1e6}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	return p, s
}

// d032RejectAll 全拒执行器（零成交 FAILED 形态：maker 撤单/全腿被拒）。
type d032RejectAll struct{ venue string }

func (e *d032RejectAll) Key() string                                         { return e.venue }
func (e *d032RejectAll) CloseOrder(context.Context, OrderRef) (Fill, error)  { return Fill{}, nil }
func (e *d032RejectAll) TradeModeNow(context.Context, string) (int64, error) { return 1, nil }
func (e *d032RejectAll) PlaceOrder(context.Context, Order) (Fill, error) {
	return Fill{}, fmt.Errorf("fake venue reject (zero-fill)")
}

// d032FillOne 恒成交执行器（price/qty 可控）。
type d032FillOne struct {
	venue string
	px    float64
}

func (e *d032FillOne) Key() string                                         { return e.venue }
func (e *d032FillOne) CloseOrder(context.Context, OrderRef) (Fill, error)  { return Fill{}, nil }
func (e *d032FillOne) TradeModeNow(context.Context, string) (int64, error) { return 1, nil }
func (e *d032FillOne) PlaceOrder(_ context.Context, o Order) (Fill, error) {
	return Fill{OrderID: "d032-" + e.venue, Price: e.px, Qty: o.Qty}, nil
}

// T1：全腿拒 → FAILED 且零 FILLED 腿 → markOpenIncident 不打标 → 事故闸不拦。
func TestD032_ZeroFillFailedNotIncident(t *testing.T) {
	p, s := d032Pipeline(t, &d032RejectAll{venue: "3001157:mt5"}, &d032RejectAll{venue: "okx:perp"})
	res, err := p.Execute(context.Background(), "PERP:XAUUSD:3001157:mt5:okx:perp", "req-d032-z", "PERP_FUNDING", d032Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("双腿全拒应 FAILED, got %s", res.Status)
	}
	var ek *string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT exit_kind FROM positions WHERE id=$1`, res.PositionID).Scan(&ek); err != nil {
		t.Fatalf("read exit_kind: %v", err)
	}
	if ek != nil {
		t.Fatalf("零成交 FAILED 不应打事故标, got exit_kind=%s", *ek)
	}
	if id, err := s.IncidentBlockerID(context.Background(), 0); err != nil || id != 0 {
		t.Fatalf("零成交 FAILED 不应拦事故闸, blocker=%d err=%v", id, err)
	}
}

// T2：一腿成交一腿拒 → LEGGED → 打 legged_unwind → 事故闸拦。
func TestD032_LeggedMarkedIncident(t *testing.T) {
	p, s := d032Pipeline(t, &d032FillOne{venue: "3001157:mt5", px: 100}, &d032RejectAll{venue: "okx:perp"})
	res, err := p.Execute(context.Background(), "PERP:XAUUSD:3001157:mt5:okx:perp", "req-d032-l", "PERP_FUNDING", d032Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var ek *string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT exit_kind FROM positions WHERE id=$1`, res.PositionID).Scan(&ek); err != nil {
		t.Fatalf("read exit_kind: %v", err)
	}
	// LEGGED 会走 autoFlatten 尝试收平（fake CloseOrder 返回零值 Fill 即成）——
	// 终态可能转 CLOSED，但 exit_kind 恒应为 legged_unwind。
	if ek == nil || *ek != pgstore.ExitKindLeggedUnwind {
		t.Fatalf("LEGGED 应打 legged_unwind, exit_kind=%v", ek)
	}
	if id, err := s.IncidentBlockerID(context.Background(), 0); err != nil || id == 0 {
		t.Fatalf("事故仓应拦事故闸, blocker=%d err=%v", id, err)
	}
}

// T3：XASSET_CARRY 真实开仓路径 → positions.notional_usd=Σ腿名义。
// mt 腿 0.4 lots×CS50×$100=$2000（CS 由 fakeCSResolver 给 50）；okx 腿 20 张×CS1×$100=$2000 → Σ=$4000。
func TestD032_OpenNotionalWritten(t *testing.T) {
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
	reg.Register(&d032FillOne{venue: "3001157:mt5", px: 100})
	reg.Register(&d032FillOne{venue: "okx:perp", px: 100})
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
		MaxLongQty: 10, MaxShortQty: 100, MaxGrossUSD: 5000, // <DefaultCanonicalCapUSD 6000
	}})
	p.SetMarginInputs(nil, nil)
	legs := []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.4, PriceExpected: 100},
		{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 20, PriceExpected: 100},
	}
	res, err := p.Execute(ctx, oppID, "req-d032-n", "XASSET_CARRY", legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("双腿等量成交应 OPEN, got %s", res.Status)
	}
	// GetPosition SELECT 不含 notional_usd——直查列。
	var nusd *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT notional_usd FROM positions WHERE id=$1`, res.PositionID).Scan(&nusd); err != nil {
		t.Fatalf("read notional_usd: %v", err)
	}
	if nusd == nil || *nusd != 4000 {
		t.Fatalf("notional_usd 应为 Σ腿名义 $4000, got %v", nusd)
	}
	// 敞口读口同源验证：两 venue 各摊 $2000。
	expo, err := s.VenueNotionalExposure(ctx, []string{"XASSET_CARRY", "FUNDING_HEDGE"})
	if err != nil {
		t.Fatalf("exposure: %v", err)
	}
	if expo["okx:perp"] != 2000 || expo["3001157:mt5"] != 2000 {
		t.Fatalf("venue 敞口应各 $2000, got %v", expo)
	}
}
