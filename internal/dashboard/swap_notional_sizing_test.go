package dashboard

import (
	"math"
	"testing"
)

// TestSwapArbNotionalSizing D-097 R2 表驱动四 case：
// ① $1570/ratio 0.5/step 0.01 → 过门（sizing $785≥$716）
// ② sizing $300<min $716 → skip
// ③ BareCap cap 生效（notional > minEq×BareCap → cap）
// ④ 离线币种 fail-closed 回归（纯函数层不测，测 swapArbBareCapValue 边界）
func TestSwapArbNotionalSizing(t *testing.T) {
	// R1 取证后 minNotional ≈ $716（0.01 手 × 100000 CS × 113.5 ÷ 150 USDJPY）。
	const minNotional01Lot = 716.0

	cases := []struct {
		name         string
		minEq        float64
		minNotional  float64
		ratio        float64
		bareCap      float64
		wantOK       bool
		wantNotional float64
	}{
		{
			name:         "pass_1570_ratio05_step001",
			minEq:        1570.0,
			minNotional:  minNotional01Lot,
			ratio:        0.5,
			bareCap:      1.0,
			wantOK:       true,
			wantNotional: 785.0, // 1570×0.5=785 ≥ 716, 785 < 1570×1.0=1570 不 cap
		},
		{
			name:         "skip_sizing300_lt_min716",
			minEq:        600.0, // 600×0.5=300 < 716
			minNotional:  minNotional01Lot,
			ratio:        0.5,
			bareCap:      1.0,
			wantOK:       false,
			wantNotional: 0,
		},
		{
			name:         "cap_bareCap05_limits_notional",
			minEq:        10000.0, // 10000×0.5=5000, but bareCap=0.5 → cap to 10000×0.5=5000 (same, no cap)
			minNotional:  minNotional01Lot,
			ratio:        1.0, // ratio=1 → sizing=10000
			bareCap:      0.5, // bareLimit=10000×0.5=5000 < 10000 → cap to 5000
			wantOK:       true,
			wantNotional: 5000.0,
		},
		{
			name:         "cap_bareCap0_no_limit",
			minEq:        50000.0,
			minNotional:  minNotional01Lot,
			ratio:        1.0,
			bareCap:      0, // ≤0 = 不 cap
			wantOK:       true,
			wantNotional: 50000.0, // ratio=1, no cap
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := swapArbNotionalSizing(c.minEq, c.minNotional, c.ratio, c.bareCap)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if ok && math.Abs(got-c.wantNotional) > 0.01 {
				t.Errorf("notional=%.2f want %.2f", got, c.wantNotional)
			}
		})
	}
}

// TestSwapArbBareCapValue 边界：≤0 → 0（不 cap），>0 → 原值。
func TestSwapArbBareCapValue(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{-1, 0},
		{1.0, 1.0},
		{0.5, 0.5},
		{2.0, 2.0},
	}
	for _, c := range cases {
		if got := swapArbBareCapValue(c.in); got != c.want {
			t.Errorf("swapArbBareCapValue(%v)=%v want %v", c.in, got, c.want)
		}
	}
}
