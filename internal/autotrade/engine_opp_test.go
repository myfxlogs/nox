package autotrade

import (
	"math"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeCSResolver 构造 (canonical,venueKey)→CS 的内存 resolver，未命中等价 CS 未知。
func fakeCSResolver(table map[string]float64) func(canonical, venueKey string) (float64, bool) {
	return func(canonical, venueKey string) (float64, bool) {
		if v, ok := table[venueKey+"/"+canonical]; ok && v > 0 {
			return v, true
		}
		return 0, false
	}
}

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-4
}

// TestComputeBuyQty 五用例（D-135 §3 验收）：先红后绿——
// 用例 1 旧代码（漏乘 CS）=12.09（红），修后乘 CS=0.0121（绿）。
func TestComputeBuyQty(t *testing.T) {
	const notional = 1000.0
	mtOilCS := fakeCSResolver(map[string]float64{"ic:mt5/USOIL": 1000})
	mtFxCS := fakeCSResolver(map[string]float64{"ic:mt5/EURUSD": 100000})
	emptyCS := fakeCSResolver(map[string]float64{})       // MT 腿查不到 → skip
	nilResolver := func(string, string) (float64, bool) { // nil 接线等价
		return 0, false
	}

	cases := []struct {
		name      string
		opp       pgstore.Opportunity
		resolver  func(canonical, venueKey string) (float64, bool)
		wantQty   float64 // skip=false 时期望
		wantSkip  bool
		redQty    float64 // 旧代码（漏乘 CS）期望——证明红
		expectRed bool
	}{
		{
			name:      "1 MT 油腿 USOIL CS=1000 → qty=0.0121（修后绿；旧=12.09 红）",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			resolver:  mtOilCS,
			wantQty:   0.0121,
			wantSkip:  false,
			redQty:    12.09,
			expectRed: true,
		},
		{
			name:     "2 MT FX 腿 EURUSD CS=100000 → qty=0.00926",
			opp:      pgstore.Opportunity{ID: "x", Canonical: "EURUSD", VenueBuy: "ic:mt5", PriceBuy: 1.08},
			resolver: mtFxCS,
			wantQty:  0.00926,
			wantSkip: false,
		},
		{
			name:     "3 CEX 腿 ETHUSDT → qty=notional/price 行为不变（回归）",
			opp:      pgstore.Opportunity{ID: "x", Canonical: "ETHUSDT", VenueBuy: "binance:perp", PriceBuy: 2500},
			resolver: mtOilCS, // CEX 腿不查 resolver，cs=1
			wantQty:  1000.0 / 2500,
			wantSkip: false,
		},
		{
			name:     "4 MT 腿 CS 查不到 → skip（fail-closed）",
			opp:      pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			resolver: emptyCS,
			wantSkip: true,
		},
		{
			name:     "5 oppContractSize 未接线（nil）→ MT 腿 skip、CEX 腿正常",
			opp:      pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			resolver: nilResolver,
			wantSkip: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qty, skip := computeBuyQty(c.opp, notional, c.resolver)
			if skip != c.wantSkip {
				t.Fatalf("%s: skip=%v want=%v", c.name, skip, c.wantSkip)
			}
			if c.wantSkip {
				return
			}
			if !approxEq(qty, c.wantQty) {
				t.Errorf("%s: qty=%.6f want=%.6f", c.name, qty, c.wantQty)
			}
			// 红证据：旧公式（漏乘 CS）应产出 redQty（用例 1≈12.09，2 位小数显示值）。
			if c.expectRed {
				oldQty := notional / c.opp.PriceBuy
				if math.Abs(oldQty-c.redQty) > 1e-2 {
					t.Errorf("%s: 旧代码红证据 oldQty=%.4f want≈%.2f", c.name, oldQty, c.redQty)
				}
				t.Logf("%s: 旧代码（漏乘 CS）qty=%.4f=红；修后 qty=%.6f=绿", c.name, oldQty, qty)
			}
		})
	}
}

// TestComputeBuyQty_NilResolverCEXNormal 用例 5 第二半：nil 接线时 CEX 腿仍正常（cs=1）。
func TestComputeBuyQty_NilResolverCEXNormal(t *testing.T) {
	opp := pgstore.Opportunity{ID: "x", Canonical: "ETHUSDT", VenueBuy: "binance:perp", PriceBuy: 2500}
	qty, skip := computeBuyQty(opp, 1000, func(string, string) (float64, bool) { return 0, false })
	if skip {
		t.Fatalf("CEX 腿 nil resolver 不应 skip")
	}
	if !approxEq(qty, 1000.0/2500) {
		t.Errorf("CEX 腿 nil resolver qty=%.6f want=%.6f", qty, 1000.0/2500)
	}
}
