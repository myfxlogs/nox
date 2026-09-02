package autotrade

import (
	"math"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeUsdPerLot 构造 (canonical,venueKey,side)→usdPerLot 的内存 fake（D-190 新签名）。
// 未命中 → (0,false) 等价 fail-closed。
func fakeUsdPerLot(table map[string]float64) func(canonical, venueKey, side string) (float64, bool) {
	return func(canonical, venueKey, side string) (float64, bool) {
		if v, ok := table[venueKey+"/"+canonical]; ok && v > 0 {
			return v, true
		}
		return 0, false
	}
}

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-4
}

// TestComputeBuyQty 五用例（D-135/D-190 §1.5 验收）：先红后绿——
// 用例 1 旧代码（漏乘 CS）=12.09（红），修后 usdPerLot=82660 → qty=0.0121（绿）。
// D-190：价格源从 opp.PriceBuy 改为 usdPerLot（live qbus ask 口径），CS 源统一到 USDPerLotForLeg。
func TestComputeBuyQty(t *testing.T) {
	const notional = 1000.0
	mtOilPerLot := fakeUsdPerLot(map[string]float64{"ic:mt5/USOIL": 1000 * 82.66})   // CS=1000 × price=82.66
	mtFxPerLot := fakeUsdPerLot(map[string]float64{"ic:mt5/EURUSD": 100000 * 1.08})  // CS=100000 × price=1.08
	cexPerLot := fakeUsdPerLot(map[string]float64{"binance:perp/ETHUSDT": 1 * 2500}) // CS=1 × price=2500
	emptyPerLot := fakeUsdPerLot(map[string]float64{})                               // MT 腿查不到 → skip
	nilPerLot := func(string, string, string) (float64, bool) { return 0, false }    // nil 接线等价

	cases := []struct {
		name      string
		opp       pgstore.Opportunity
		usdPerLot func(canonical, venueKey, side string) (float64, bool)
		wantQty   float64 // skip=false 时期望
		wantSkip  bool
		redQty    float64 // 旧代码（漏乘 CS）期望——证明红
		expectRed bool
	}{
		{
			name:      "1 MT 油腿 USOIL CS=1000 → usdPerLot=82660 → qty=0.0121（修后绿；旧=12.09 红）",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			usdPerLot: mtOilPerLot,
			wantQty:   0.0121,
			wantSkip:  false,
			redQty:    12.09,
			expectRed: true,
		},
		{
			name:      "2 MT FX 腿 EURUSD CS=100000 → usdPerLot=108000 → qty=0.00926",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "EURUSD", VenueBuy: "ic:mt5", PriceBuy: 1.08},
			usdPerLot: mtFxPerLot,
			wantQty:   0.00926,
			wantSkip:  false,
		},
		{
			name:      "3 CEX 腿 ETHUSDT → usdPerLot=2500 → qty=notional/usdPerLot 行为不变（回归）",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "ETHUSDT", VenueBuy: "binance:perp", PriceBuy: 2500},
			usdPerLot: cexPerLot,
			wantQty:   1000.0 / 2500,
			wantSkip:  false,
		},
		{
			name:      "4 MT 腿 usdPerLot 查不到 → skip（fail-closed）",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			usdPerLot: emptyPerLot,
			wantSkip:  true,
		},
		{
			name:      "5 usdPerLot 未接线（nil）→ MT 腿 skip、CEX 腿正常",
			opp:       pgstore.Opportunity{ID: "x", Canonical: "USOIL", VenueBuy: "ic:mt5", PriceBuy: 82.66},
			usdPerLot: nilPerLot,
			wantSkip:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qty, skip := computeBuyQty(c.opp, notional, c.usdPerLot)
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

// TestComputeBuyQty_NilResolverCEXNormal 用例 5 第二半：nil 接线时 CEX 腿仍正常。
// D-190：CEX 腿现在也要求 usdPerLot 返回 ok（detector 产 opp 前提下 qbus 必有报价）。
func TestComputeBuyQty_NilResolverCEXNormal(t *testing.T) {
	opp := pgstore.Opportunity{ID: "x", Canonical: "ETHUSDT", VenueBuy: "binance:perp", PriceBuy: 2500}
	// CEX 腿 usdPerLot 返回 ok（模拟 qbus 有报价）
	usdPerLot := func(c, v, s string) (float64, bool) { return 2500, true }
	qty, skip := computeBuyQty(opp, 1000, usdPerLot)
	if skip {
		t.Fatalf("CEX 腿 usdPerLot ok 不应 skip")
	}
	if !approxEq(qty, 1000.0/2500) {
		t.Errorf("CEX 腿 qty=%.6f want=%.6f", qty, 1000.0/2500)
	}
}

// TestComputeBuyQty_JPYAnchor D-190 §1.5 数值锚：
// AUDJPY notionalUSD=1000、CS=100000、price=97、USDJPY=150 → usdPerLot=64666.67 → qty=0.0155。
// 修前 computeBuyQty=1000/97=10.31 手，错 ~664 倍。
func TestComputeBuyQty_JPYAnchor(t *testing.T) {
	opp := pgstore.Opportunity{ID: "x", Canonical: "AUDJPY", VenueBuy: "ic:mt5", PriceBuy: 97.0}
	usdPerLot := func(c, v, s string) (float64, bool) { return 64666.67, true } // CS×price÷USDJPY
	qty, skip := computeBuyQty(opp, 1000.0, usdPerLot)
	if skip {
		t.Fatal("JPY anchor: expected ok, got skip")
	}
	want := 1000.0 / 64666.67 // ≈0.0155
	if math.Abs(qty-want) > 1e-4 {
		t.Errorf("JPY anchor: qty=%.6f want≈%.6f", qty, want)
	}
	// 红证据：旧公式 notional/price = 1000/97 = 10.31（错 ~664 倍）
	oldQty := 1000.0 / 97.0
	t.Logf("JPY anchor: 旧代码 qty=%.4f=红（错 ~664 倍）；修后 qty=%.6f=绿", oldQty, qty)
}
