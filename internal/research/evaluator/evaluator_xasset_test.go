package evaluator

import (
	"strings"
	"testing"
	"time"

	"nox/internal/research/detector"
)

// xassetCand 构造 XASSET_CARRY 候选：买 XAUUSD@3001157:mt5 卖 PAXGUSDT@binance:perp。
// gross = detector 净息差（funding − swap 已含，v1.2 swap=0 → gross=funding）。
func xassetCand(gross float64) detector.Candidate {
	return detector.Candidate{
		Type:          detector.TypeXAssetCarry,
		Canonical:     "XAUUSD",
		CanonicalSell: "PAXGUSDT",
		AssetClass:    "GOLD",
		VenueBuy:      "3001157:mt5",
		VenueSell:     "binance:perp",
		PriceBuy:      4000.0,
		PriceSell:     4000.0,
		GrossBps:      gross,
		QuoteTime:     time.Now().UnixMilli(),
	}
}

// TestEvaluateXAssetCarry_A2 A2 合成锚（v1.2 终版）：funding 8.0 / swap 0 / margin 4.5 → net=3.5pp 落库。
func TestEvaluateXAssetCarry_A2(t *testing.T) {
	r := Evaluate(xassetCand(8.0), baseCfg(), 0.05, 0.05, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.NetBps < 3.4 || r.NetBps > 3.6 {
		t.Fatalf("NetBps = %v, want ~3.5（gross 8.0 − margin 4.5，R1=A）", r.NetBps)
	}
	if r.Rating == RatingTrap {
		t.Fatalf("Rating = %s, want non-trap（net 3.5 > 0）", r.Rating)
	}
	if r.CanonicalSell != "PAXGUSDT" || r.AssetClass != "GOLD" {
		t.Fatalf("CanonicalSell/AssetClass want PAXGUSDT/GOLD, got %q/%q", r.CanonicalSell, r.AssetClass)
	}
	if r.BreakevenDays == nil {
		t.Fatal("XASSET_CARRY should have breakeven_days")
	}
	// 卖腿滑点用 CanonicalSell=PAXGUSDT（三线 8.0，裁 4）；买腿 MT=cfg.SlippageBps 1.0 → 合计 9.0。
	// 若误用 Canonical=XAUUSD 则回落默认 3.0 → 合计 4.0。
	if r.SlippageCost < 8.9 || r.SlippageCost > 9.1 {
		t.Fatalf("SlippageCost = %v, want ~9.0（MT 1.0 + PAXGUSDT 8.0）", r.SlippageCost)
	}
}

// TestEvaluateXAssetCarry_Trap A2 反锚：funding 3.0 → net = 3.0−4.5 = −1.5 → trap 不落库。
func TestEvaluateXAssetCarry_Trap(t *testing.T) {
	r := Evaluate(xassetCand(3.0), baseCfg(), 0, 0, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap（net 3.0−4.5=−1.5 ≤ 0）", r.Rating)
	}
	if r.NetBps >= 0 {
		t.Fatalf("NetBps = %v, want < 0", r.NetBps)
	}
}

// TestEvaluateXAssetCarry_EntrySpreadBonus 有利进场价差缩短回本（entry 负项入 oneTimeCost）。
func TestEvaluateXAssetCarry_EntrySpreadBonus(t *testing.T) {
	c := xassetCand(8.0)
	cfg := baseCfg()
	base := Evaluate(c, cfg, 0.05, 0.05, 0.6, 0)
	c.PriceSell = 4004.0 // 卖价高于买价 → 进场价差 +10bps（有利）
	withBonus := Evaluate(c, cfg, 0.05, 0.05, 0.6, 0)
	if base.BreakevenDays == nil || withBonus.BreakevenDays == nil {
		t.Fatal("breakeven_days missing")
	}
	if *withBonus.BreakevenDays >= *base.BreakevenDays {
		t.Fatalf("favorable entry should shorten breakeven: base=%v bonus=%v", *base.BreakevenDays, *withBonus.BreakevenDays)
	}
}

// TestEvaluateXAssetCarry_XauusdtSlip D-303：卖腿 CanonicalSell=XAUUSDT 走一线滑点 1.0——
// SlippageCost = cfg.SlippageBps（MT 买腿 1.0）+ 1.0 = 2.0。
func TestEvaluateXAssetCarry_XauusdtSlip(t *testing.T) {
	c := xassetCand(8.0)
	c.CanonicalSell = "XAUUSDT"
	r := Evaluate(c, baseCfg(), 0.05, 0.05, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.SlippageCost < 1.9 || r.SlippageCost > 2.1 {
		t.Fatalf("SlippageCost = %v, want ~2.0（MT 1.0 + XAUUSDT 1.0）", r.SlippageCost)
	}
}

// TestSlipBpsForCEX_D308Tiers D-308 滑点分级表锚：binance 主力所按量分档（CL/BZ/XAG=2.0、
// COPPER=5.0、XPT/XPD=8.0），跨所与指数薄盘全 8.0；漏项会静默回落默认 3.0 故逐档断言。
func TestSlipBpsForCEX_D308Tiers(t *testing.T) {
	for sym, want := range map[string]float64{
		"CLUSDT": 2.0, "BZUSDT": 2.0, "XAGUSDT": 2.0,
		"COPPERUSDT": 5.0,
		"XPTUSDT":    8.0, "XPDUSDT": 8.0,
	} {
		if got := slipBpsForCEX("binance:perp", sym); got != want {
			t.Errorf("slipBpsForCEX(binance:perp, %q) = %v, want %v", sym, got, want)
		}
	}
	// venue:canonical 分级（0101 勘误：canonical 全所同键 <base>USDT，薄盘跨所腿 8.0）
	for vk, want := range map[string]float64{
		"gate:CLUSDT": 8.0, "okx:CLUSDT": 8.0, "gate:XAGUSDT": 8.0,
		"gate:XAUUSDT": 8.0, "okx:XAUUSDT": 8.0,
		"gate:NAS100USDT": 8.0, "gate:US30USDT": 8.0, "gate:SPX500USDT": 8.0,
		"okx:JP225USDT": 8.0,
	} {
		parts := strings.SplitN(vk, ":", 2)
		if got := slipBpsForCEX(parts[0]+":perp", parts[1]); got != want {
			t.Errorf("slipBpsForCEX(%s) = %v, want %v", vk, got, want)
		}
	}
}
