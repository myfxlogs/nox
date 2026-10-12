// d599_netpnl_test.go —— D-599：手续费净额化。净额四态表驱动 + pos507 回归（毛+1.40 费4.18→净−2.78）
// + NULL 不写 0 + FAILED 腿不计费 + 集成路（ClosePosition 原子写净额+fees_usd+fee_quality）。
package exec

import (
	"context"
	"fmt"
	"math"
	"testing"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

func f64(v float64) *float64 { return &v }

// TestD599_PositionFeesUSD 表驱动四态：actual/estimated/mixed/none + pos507 回归额。
func TestD599_PositionFeesUSD(t *testing.T) {
	px := 100.0
	q := 1.0
	open := func(fee *float64) pgstore.PositionLeg {
		return pgstore.PositionLeg{LegNo: 0, Phase: "OPEN", Status: "FILLED", Venue: "okx:perp",
			Symbol: "BTC-USDT-SWAP", Side: "BUY", PriceActual: &px, FilledQty: &q, AvgFillPrice: &px, FeeUSD: fee}
	}
	cls := func(fee *float64) pgstore.PositionLeg {
		return pgstore.PositionLeg{LegNo: 0, Phase: "CLOSE", Status: "FILLED", Venue: "okx:perp",
			Symbol: "BTC-USDT-SWAP", Side: "SELL", PriceActual: &px, FilledQty: &q, AvgFillPrice: &px, FeeUSD: fee}
	}
	failed := pgstore.PositionLeg{LegNo: 1, Phase: "OPEN", Status: "FAILED", Venue: "gate:perp", Symbol: "X"}

	bps5 := FeeBpsResolver(func(string) float64 { return 5.0 }) // 5bps
	noBps := FeeBpsResolver(func(string) float64 { return 0 })

	cases := []struct {
		name      string
		p         *Pipeline
		open      []pgstore.PositionLeg
		close     []pgstore.PositionLeg
		wantFees  *float64 // nil=fees_usd NULL
		wantQual  string
		tolerance float64
	}{
		{
			name: "actual：双腿实采（pos507 回归 2.09245+2.09175=4.1842）",
			p:    &Pipeline{},
			open: []pgstore.PositionLeg{open(f64(2.09245))}, close: []pgstore.PositionLeg{cls(f64(2.09175))},
			wantFees: f64(4.1842), wantQual: "actual", tolerance: 1e-6,
		},
		{
			name: "estimated：双腿无费可估（notional 100×5bps×2=0.10）",
			p:    &Pipeline{feeBps: bps5},
			open: []pgstore.PositionLeg{open(nil)}, close: []pgstore.PositionLeg{cls(nil)},
			wantFees: f64(0.10), wantQual: "estimated", tolerance: 1e-9,
		},
		{
			name: "mixed：open 实采 + close 估算",
			p:    &Pipeline{feeBps: bps5},
			open: []pgstore.PositionLeg{open(f64(2.0))}, close: []pgstore.PositionLeg{cls(nil)},
			wantFees: f64(2.05), wantQual: "mixed", tolerance: 1e-9,
		},
		{
			name: "mixed：open 实采 + close 无费无估算率（unknown 腿）",
			p:    &Pipeline{feeBps: noBps},
			open: []pgstore.PositionLeg{open(f64(2.0))}, close: []pgstore.PositionLeg{cls(nil)},
			wantFees: f64(2.0), wantQual: "mixed", tolerance: 1e-9,
		},
		{
			name: "none：无费且无估算率 → NULL 不写 0",
			p:    &Pipeline{feeBps: noBps},
			open: []pgstore.PositionLeg{open(nil)}, close: []pgstore.PositionLeg{cls(nil)},
			wantFees: nil, wantQual: "none",
		},
		{
			name: "none：全 FAILED 腿（pos503/504 形态）→ NULL",
			p:    &Pipeline{feeBps: bps5},
			open: []pgstore.PositionLeg{failed}, close: nil,
			wantFees: nil, wantQual: "none",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fees, qual, _ := tc.p.positionFeesUSD(tc.open, tc.close, 1.0)
			if qual != tc.wantQual {
				t.Fatalf("quality=%q want %q", qual, tc.wantQual)
			}
			if tc.wantFees == nil {
				if fees != nil {
					t.Fatalf("fees=%v want NULL", *fees)
				}
				return
			}
			if fees == nil {
				t.Fatalf("fees=NULL want %v", *tc.wantFees)
			}
			if math.Abs(*fees-*tc.wantFees) > tc.tolerance {
				t.Fatalf("fees=%v want %v", *fees, *tc.wantFees)
			}
		})
	}
}

// TestD599_CloseLegFeeUSD 平仓腿三源：FeeUSD 优先 → Commission×fx 回落 → NULL。
func TestD599_CloseLegFeeUSD(t *testing.T) {
	if v := closeLegFeeUSD(Fill{FeeUSD: 2.5}, 1.0); v == nil || *v != 2.5 {
		t.Fatalf("FeeUSD 优先: %v", v)
	}
	if v := closeLegFeeUSD(Fill{Commission: -3.5}, 1.0); v == nil || *v != 3.5 {
		t.Fatalf("MT commission 负值→绝对值: %v", v)
	}
	if v := closeLegFeeUSD(Fill{Commission: 2.0}, 1.5); v == nil || *v != 3.0 {
		t.Fatalf("commission×fx: %v", v)
	}
	if v := closeLegFeeUSD(Fill{}, 1.0); v != nil {
		t.Fatalf("无费→NULL 不写 0: %v", *v)
	}
}

// seqExec 按调用序返回填充（价/费逐次指定）——pos507 回归集成路用。
type seqExec struct {
	venue string
	fills []Fill
	idx   int
}

func (e *seqExec) Key() string { return e.venue }
func (e *seqExec) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	f := e.fills[e.idx%len(e.fills)]
	e.idx++
	if f.Qty <= 0 {
		f.Qty = o.Qty
	}
	if f.OrderID == "" {
		f.OrderID = fmt.Sprintf("seq-%s-%d", o.Side, e.idx)
	}
	return f, nil
}
func (e *seqExec) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, fmt.Errorf("cex: use PlaceOrder")
}

// TestD599_NetPnlIntegration pos507 形态回归：毛 +1.40、费 4.18 → 净 −2.78 原子落库。
// 双腿 CEX 仓：okx 腿 open 100 → close 101.4（qty=1 毛+1.40，费 open 2.09245+close 2.09175），
// binance 腿平价 100→100（毛 0，费 0.001×2）。realized_pnl=净额+fees_usd+fee_quality 三列断言。
func TestD599_NetPnlIntegration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&seqExec{venue: "okx:perp", fills: []Fill{
		{Price: 100.0, FeeUSD: 2.09245}, // open
		{Price: 101.4, FeeUSD: 2.09175}, // close
	}})
	reg.Register(&seqExec{venue: "binance:perp", fills: []Fill{
		{Price: 100.0, FeeUSD: 0.001},
		{Price: 100.0, FeeUSD: 0.001},
	}})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
		"okx:perp":     {{Symbol: "BTC-USDT-SWAP", Side: "BUY", Ticket: "o-open"}},
		"binance:perp": {{Symbol: "BTCUSDT", Side: "SELL", Ticket: "b-open"}},
	}}

	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:599", "req-d599", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 1, PriceExpected: 100},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 1, PriceExpected: 100},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("status=%s want OPEN", res.Status)
	}
	if err := p.ClosePosition(ctx, res.PositionID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	var realized, fees float64
	var qual string
	err = s.Pool().QueryRow(ctx,
		`SELECT realized_pnl, fees_usd, fee_quality FROM positions WHERE id=$1`, res.PositionID,
	).Scan(&realized, &fees, &qual)
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	// 毛 = (101.4−100)×1 + (100−100)×1×(−1) = 1.40；费 = 2.09245+2.09175+0.001+0.001 = 4.1862
	wantNet := 1.4 - 4.1862
	if math.Abs(realized-wantNet) > 1e-6 {
		t.Fatalf("realized_pnl=%.6f want net %.6f（毛+1.40−费4.1862，pos507 型）", realized, wantNet)
	}
	if math.Abs(fees-4.1862) > 1e-6 {
		t.Fatalf("fees_usd=%.6f want 4.1862", fees)
	}
	if qual != "actual" {
		t.Fatalf("fee_quality=%q want actual", qual)
	}
}

// TestD599_EstimatedIntegration NULL 费腿 + 估算率 → fees_usd 估算入账 + fee_quality='estimated'。
func TestD599_EstimatedIntegration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	reg.Register(&seqExec{venue: "okx:perp", fills: []Fill{
		{Price: 100.0}, {Price: 101.0},
	}})
	reg.Register(&seqExec{venue: "binance:perp", fills: []Fill{
		{Price: 100.0}, {Price: 100.0},
	}})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.SetFeeBpsResolver(func(string) float64 { return 5.0 }) // 5bps/腿
	p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
		"okx:perp":     {{Symbol: "BTC-USDT-SWAP", Side: "BUY", Ticket: "o-open"}},
		"binance:perp": {{Symbol: "BTCUSDT", Side: "SELL", Ticket: "b-open"}},
	}}
	res, err := p.Execute(ctx,
		"PERP:BTCUSDT:okx:perp:binance:perp:599e", "req-d599e", "PERP_FUNDING",
		[]LegSpec{
			{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 1, PriceExpected: 100},
			{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "SELL", Qty: 1, PriceExpected: 100},
		})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if err := p.ClosePosition(ctx, res.PositionID); err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	var realized, fees float64
	var qual string
	err = s.Pool().QueryRow(ctx,
		`SELECT realized_pnl, fees_usd, fee_quality FROM positions WHERE id=$1`, res.PositionID,
	).Scan(&realized, &fees, &qual)
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	// notional/腿=100×1×1×1=100（close 腿价 101/100）；4 腿全估 → fees≈0.1005+0.1005…
	// open 两腿 px=100×100bps=0.05 each；close okx px=101→0.0505、binance 100→0.05 → 总≈0.201
	if qual != "estimated" {
		t.Fatalf("fee_quality=%q want estimated", qual)
	}
	if fees <= 0.19 || fees > 0.21 {
		t.Fatalf("fees_usd=%.6f want ~0.201（4腿×100notional×5bps）", fees)
	}
	// 毛 = +1.0；净 = 毛 − fees
	if math.Abs(realized-(1.0-fees)) > 1e-9 {
		t.Fatalf("realized=%.6f want gross 1.0 − fees %.6f", realized, fees)
	}
}
