// d079_pipeline_leverage_test.go —— D-079 锚③：pipeline 开仓前置 leverage ensure。
// (e) perp 腿 ensure 败 → Execute error+零下单+零 position 行；(f) target=0 → 不调用；
// 附带：未实现 leverageSetter 的 perp 腿 executor 跳过不误伤。
package exec

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arb/internal/data/bus"
)

// d079LeverageExec perp 腿 fake：实现 leverageSetter 记录调用面（symbol/sellLeg/target）。
type d079LeverageExec struct {
	venue       string
	fill        Fill
	ensureErr   error
	ensureCalls int
	lastSymbol  string
	lastSell    bool
	lastTarget  float64
	placed      bool
}

func (e *d079LeverageExec) Key() string { return e.venue }
func (e *d079LeverageExec) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.placed = true
	return e.fill, nil
}
func (e *d079LeverageExec) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return e.fill, nil
}
func (e *d079LeverageExec) EnsureVenueLeverage(ctx context.Context, symbol string, sellLeg bool, target float64) error {
	e.ensureCalls++
	e.lastSymbol, e.lastSell, e.lastTarget = symbol, sellLeg, target
	return e.ensureErr
}

// TestD079_EnsureFailBlocksOpen (e)：perp 腿 ensure 返 err → Execute error，
// 零下单零 position 行（建 position 前拒=零半腿）。
func TestD079_EnsureFailBlocksOpen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	bad := &d079LeverageExec{venue: "okx:perp", ensureErr: errors.New("venue set-leverage 503")}
	good := &d079LeverageExec{venue: "binance:perp", fill: Fill{OrderID: "b1", Price: 100, Qty: 1}}
	reg := NewRegistry()
	reg.Register(bad)
	reg.Register(good)
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.SetPerpLeverageTarget(10)

	_, err := p.Execute(ctx, "opp:d079a", "req-d079a", "FUNDING_HEDGE", []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 100},
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 1, PriceExpected: 100},
	})
	if err == nil || !strings.Contains(err.Error(), "leverage ensure leg okx:perp") {
		t.Fatalf("ensure 败应拒开, got %v", err)
	}
	if bad.ensureCalls != 1 || !bad.lastSell || bad.lastSymbol != "BTC-USDT-SWAP" || bad.lastTarget != 10 {
		t.Fatalf("ensure 调用面错: calls=%d sym=%q sell=%v target=%v", bad.ensureCalls, bad.lastSymbol, bad.lastSell, bad.lastTarget)
	}
	if bad.placed || good.placed {
		t.Fatal("ensure 败→零下单（防半腿）")
	}
	if positions, _ := s.ListPositions(ctx, "", 10, 0); len(positions) > 0 {
		t.Fatalf("ensure 败→零 position 行, got %d", len(positions))
	}
}

// TestD079_EnsureTargetZeroSkips (f)：target≤0 → ensure 一次不调（旧行为不变）。
// 未实现 leverageSetter 的 perp executor（d415CaptureExecutor）→ 跳过不误伤。
func TestD079_EnsureTargetZeroSkips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reg := NewRegistry()
	ens := &d079LeverageExec{venue: "okx:perp", fill: Fill{OrderID: "o1", Price: 100, Qty: 1}}
	plain := &d415CaptureExecutor{venue: "binance:perp"}
	reg.Register(ens)
	reg.Register(plain)

	// target=0：ensure 全跳过（下游 cap 守卫 err 与否与本断言无关——只钉调用面）。
	p0 := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	p0.SetPerpLeverageTarget(0)
	_, _ = p0.Execute(ctx, "opp:d079b", "req-d079b", "FUNDING_HEDGE", []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 100},
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 1, PriceExpected: 100},
	})
	if ens.ensureCalls != 0 {
		t.Fatalf("target=0 应零 ensure 调用, got %d", ens.ensureCalls)
	}

	// target>0：实现的腿被 ensure、未实现的腿跳过（接口缝语义）。
	p1 := NewPipeline(reg, s, bus.New(), RiskLimits{}, "demo")
	p1.SetPerpLeverageTarget(10)
	_, _ = p1.Execute(ctx, "opp:d079c", "req-d079c", "FUNDING_HEDGE", []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 100},
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 1, PriceExpected: 100},
	})
	if ens.ensureCalls != 1 {
		t.Fatalf("target=10 应恰 1 次 ensure（未实现腿跳过）, got %d", ens.ensureCalls)
	}
}
