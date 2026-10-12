// d036_margin_spec_test.go —— D-036 验收锚：MT5 仓位级保证金=venue 规格估算。
// 公式：InitialMargin>0 → InitialMargin×lots；否则 lots×ContractSize×openPrice×init_margin_rate。
// 票锚：3001157 XAUUSDm 0.03 lot @4237.56 × CS100 × imr 0.0005 = $6.3563（≈$6.36）；
// init_margin_rate 缺 → 诚实空态。margin_is_estimate 标记随 proto 输出。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/venue"
)

// TestD036_MarginEstimateFormula 估算式双径 + 空态语义。
func TestD036_MarginEstimateFormula(t *testing.T) {
	// 率径：0.03×100×4237.56×0.0005 = 6.35634 ≈ $6.36
	v, ok := estimateMargin(symMarginSpec{contractSize: 100}, 0.0005, true, 0.03, 4237.56)
	if !ok || math.Abs(v-6.35634) > 0.001 {
		t.Fatalf("率径 margin=%v ok=%v want≈6.36", v, ok)
	}
	// 定额径：InitialMargin>0 优先（直乘 lots）
	v, ok = estimateMargin(symMarginSpec{initialMargin: 50, contractSize: 100}, 0.0005, true, 0.03, 4237.56)
	if !ok || v != 1.5 {
		t.Fatalf("定额径 margin=%v ok=%v want 1.5", v, ok)
	}
	// init_margin_rate 缺 → 率径不可用 → 空（不猜）
	if _, ok = estimateMargin(symMarginSpec{contractSize: 100}, 0, false, 0.03, 4237.56); ok {
		t.Fatal("imr 缺应空态")
	}
	// 规格全缺 → 空
	if _, ok = estimateMargin(symMarginSpec{}, 0.0005, true, 0.03, 4237.56); ok {
		t.Fatal("规格缺应空态")
	}
}

// TestD036_ProtoForMarginFlag protoFor：MT5 行 margin=规格估算值 + margin_is_estimate=true；
// imr 缺 → margin 空 + flag=false；CEX 行原生 iw 不置 flag。
func TestD036_ProtoForMarginFlag(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, 0, nil, ServerTZCache{}, nil, nil)
	now := time.Now()
	ord := venue.Order{
		Venue: "3001157", VenueKey: "3001157:mt5", Symbol: "XAUUSDm",
		Side: "BUY", Volume: 0.03, OpenPrice: 4237.56,
	}

	// 规格+imr 齐 → 估算值 6.3563 + flag
	vs := venueState{
		orders: map[string]venue.Order{"t1": ord},
		marginSpec: map[string]symMarginSpec{
			"XAUUSDm": {contractSize: 100},
		},
		imr: 0.0005, imrOK: true,
	}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, now)
	if math.Abs(pb.Margin-6.35634) > 0.001 {
		t.Fatalf("margin=%v want≈6.36", pb.Margin)
	}
	if !pb.MarginIsEstimate {
		t.Fatal("margin_is_estimate 应=true")
	}

	// imr 缺 → 诚实空态（margin=0、flag 不置）
	vs.imrOK, vs.imr = false, 0
	pb = m.protoFor(context.Background(), "3001157:mt5", vs, ord, now)
	if pb.Margin != 0 || pb.MarginIsEstimate {
		t.Fatalf("imr 缺 margin=%v flag=%v want 0/false", pb.Margin, pb.MarginIsEstimate)
	}

	// CEX 行：原生 iw 直显不置 flag
	cexOrd := venue.Order{
		Venue: "okx", VenueKey: "okx:perp", Symbol: "ETH-USDT-SWAP",
		Side: "SELL", Volume: 1, OpenPrice: 3000, InitialMargin: 300,
	}
	vsCEX := venueState{orders: map[string]venue.Order{"t2": cexOrd}, online: true}
	m2 := NewBrokerPositionMonitor(nil, bus.New(), 0, nil, ServerTZCache{}, nil, nil)
	pb = m2.protoFor(context.Background(), "okx:perp", vsCEX, cexOrd, now)
	if pb.Margin != 300 {
		t.Fatalf("CEX margin=%v want 300", pb.Margin)
	}
	if pb.MarginIsEstimate {
		t.Fatal("CEX 原生 iw 不应置估算标记")
	}
}
