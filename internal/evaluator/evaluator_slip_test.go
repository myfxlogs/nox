package evaluator

import (
	"testing"

	"arb/internal/detector"
)

// TestSlipBpsForCEX D-145 §②：CEX 滑点品种分级表。
// 一线 ETH/BTC/BNB=1.0；二线 DOT/AVAX/FIL/SOL/XRP/ADA=3.0；三线 TRX/ATOM/NEAR=8.0；未知=3.0。
func TestSlipBpsForCEX(t *testing.T) {
	cases := []struct {
		canonical string
		want      float64
		tier      string
	}{
		{"ETHUSDT", 1.0, "一线"},
		{"BTCUSDT", 1.0, "一线"},
		{"BNBUSDT", 1.0, "一线"},
		{"DOTUSDT", 3.0, "二线"},
		{"AVAXUSDT", 3.0, "二线"},
		{"FILUSDT", 3.0, "二线"},
		{"SOLUSDT", 3.0, "二线"},
		{"XRPUSDT", 3.0, "二线"},
		{"ADAUSDT", 3.0, "二线"},
		{"TRXUSDT", 8.0, "三线"},
		{"ATOMUSDT", 8.0, "三线"},
		{"NEARUSDT", 8.0, "三线"},
		{"UNKNOWNUSDT", cexSlipDefaultBps, "未知回落"},
		{"", cexSlipDefaultBps, "空 canonical 回落"},
	}
	for _, tc := range cases {
		got := slipBpsForCEX(tc.canonical)
		if got != tc.want {
			t.Errorf("slipBpsForCEX(%s) = %v, want %v (%s)", tc.canonical, got, tc.want, tc.tier)
		}
	}
}

// TestSlipForLeg D-145 §②：per-leg 滑点按 venueKey 后缀判定。
// MT 腿（:mt4/:mt5）→ cfg.SlippageBps；CEX 腿（:perp/:spot）→ slipBpsForCEX。
func TestSlipForLeg(t *testing.T) {
	mtSlip := 0.3 // cfg.SlippageBps 观察期值
	cases := []struct {
		venueKey  string
		canonical string
		want      float64
		desc      string
	}{
		{"277842155:mt5", "AUDJPY", mtSlip, "MT5 腿用 cfg.SlippageBps"},
		{"1314615:mt4", "EURUSD", mtSlip, "MT4 腿用 cfg.SlippageBps"},
		{"okx:perp", "ETHUSDT", 1.0, "CEX perp 一线"},
		{"okx:perp", "NEARUSDT", 8.0, "CEX perp 三线"},
		{"binance:spot", "DOTUSDT", 3.0, "CEX spot 二线"},
		{"gate:perp", "FOOUSDT", cexSlipDefaultBps, "CEX 未知品种回落"},
	}
	for _, tc := range cases {
		got := slipForLeg(tc.venueKey, tc.canonical, mtSlip)
		if got != tc.want {
			t.Errorf("slipForLeg(%s, %s) = %v, want %v (%s)", tc.venueKey, tc.canonical, got, tc.want, tc.desc)
		}
	}
}

// TestEvaluatePerLegSlippageMTRegression D-145 §②回归锚：纯 MT 组合 slippage 仍为 cfg.SlippageBps×2。
// 观察期铁证——改 CEX 滑点不得穿透 SWAPARB（MT 腿）判定。
func TestEvaluatePerLegSlippageMTRegression(t *testing.T) {
	cfg := baseCfg() // SlippageBps=1.0
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  50.0,
		QuoteTime: 0,
	}
	r := Evaluate(c, cfg, 0, 0, 0.6, 0)
	if r.SlippageCost != 2.0 {
		t.Fatalf("MT 腿 SlippageCost = %v, want 2.0 (cfg.SlippageBps 1.0 × 2 腿，回归锚)", r.SlippageCost)
	}
}

// TestEvaluatePerLegSlippageCEX D-145 §②：CEX 腿用品种分级表。
// ETHUSDT 双 CEX 腿 → 1.0+1.0=2.0；NEARUSDT 双 CEX 腿 → 8.0+8.0=16.0。
func TestEvaluatePerLegSlippageCEX(t *testing.T) {
	cfg := baseCfg() // SlippageBps=1.0（MT 值，CEX 腿不应使用）

	// ETHUSDT 双 CEX perp 腿
	cETH := detector.Candidate{
		Type:      detector.TypePerpFunding,
		Canonical: "ETHUSDT",
		VenueBuy:  "okx:perp",
		VenueSell: "gate:perp",
		GrossBps:  10.0,
		QuoteTime: 0,
	}
	rETH := Evaluate(cETH, cfg, 0, 0, 0.6, 0)
	if rETH.SlippageCost != 2.0 {
		t.Fatalf("ETHUSDT CEX 腿 SlippageCost = %v, want 2.0 (1.0×2 一线)", rETH.SlippageCost)
	}

	// NEARUSDT 双 CEX perp 腿
	cNEAR := detector.Candidate{
		Type:      detector.TypePerpFunding,
		Canonical: "NEARUSDT",
		VenueBuy:  "okx:perp",
		VenueSell: "gate:perp",
		GrossBps:  10.0,
		QuoteTime: 0,
	}
	rNEAR := Evaluate(cNEAR, cfg, 0, 0, 0.6, 0)
	if rNEAR.SlippageCost != 16.0 {
		t.Fatalf("NEARUSDT CEX 腿 SlippageCost = %v, want 16.0 (8.0×2 三线)", rNEAR.SlippageCost)
	}
}

// TestEvaluatePerLegSlippageMixed D-145 §②：混合腿 MT×CEX 不再误伤。
// MT 腿用 cfg.SlippageBps（0.3），CEX 腿用品种表——各腿独立判定。
func TestEvaluatePerLegSlippageMixed(t *testing.T) {
	cfg := baseCfg() // SlippageBps=1.0
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "ETHUSDT",
		VenueBuy:  "a:mt5",    // MT 腿 → 1.0
		VenueSell: "okx:perp", // CEX 腿 ETHUSDT → 1.0
		GrossBps:  50.0,
		QuoteTime: 0,
	}
	r := Evaluate(c, cfg, 0, 0, 0.6, 0)
	if r.SlippageCost != 2.0 {
		t.Fatalf("混合腿 MT×CEX SlippageCost = %v, want 2.0 (MT 1.0 + CEX ETH 1.0)", r.SlippageCost)
	}
}
