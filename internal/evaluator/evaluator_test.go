package evaluator

import (
	"testing"
	"time"

	"arb/internal/detector"
)

func baseCfg() Config {
	return Config{
		SlippageBps:          1.0,
		MarginRateAnnual:     4.5,
		DetectorFreshnessTTL: 60 * time.Second,
		OpportunityTTL:       30 * time.Second,
		GrabMinNetAnnualized: 4.5,
	}
}

// TestEvaluateCrossVenue 价差型：gross 已含双边点差 → 净收益不扣 spread（D-035 双重计算修正）。
func TestEvaluateCrossVenue(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18, // 检测器用可执行价算：spread 已含
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.5, 0.5, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.Rating != RatingGrab {
		t.Fatalf("Rating = %s, want grab", r.Rating)
	}
	// D-078 round-trip：net = 8.18 - (0.6+2.0)*2 = 8.18 - 5.2 = 2.98（开仓+平仓各付佣金+滑点）
	if r.NetBps < 2.8 || r.NetBps > 3.2 {
		t.Fatalf("NetBps = %v, want ~2.98 (round-trip cost)", r.NetBps)
	}
	if r.BreakevenDays != nil {
		t.Fatal("CrossVenue should not have breakeven_days")
	}
}

// TestEvaluateCarry 息差型：净年化 = gross − margin_rate，breakeven 用净年化推日息差（D-036）。
func TestEvaluateCarry(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  10.0, // 年化百分点（D-036 单位统一）
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	cfg.GrabMinNetAnnualized = 4.0
	r := Evaluate(c, cfg, 0.5, 0.5, 0.6, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	// net = 10 - 4.5 = 5.5%
	if r.NetBps < 5.4 || r.NetBps > 5.6 {
		t.Fatalf("NetBps = %v, want ~5.5（gross 10 − margin 4.5）", r.NetBps)
	}
	if r.BreakevenDays == nil {
		t.Fatal("Carry should have breakeven_days")
	}
	// D-078 round-trip: dailyNetBps = 5.5/100×10⁴/365 ≈ 1.507; oneTime = (1.0+0.6+2.0)*2 = 7.2; bd ≈ 4.78
	if *r.BreakevenDays < 4.6 || *r.BreakevenDays > 5.0 {
		t.Fatalf("BreakevenDays = %v, want ~4.78 (round-trip cost)", *r.BreakevenDays)
	}
}

// TestEvaluateCarryMarginDeducted carry 净收益扣资金机会成本（与 funding 一致，D-036）。
func TestEvaluateCarryMarginDeducted(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "X",
		VenueBuy:  "a:mt5",
		VenueSell: "b:mt5",
		GrossBps:  4.0, // 刚过 carry 门槛 4.0，扣 4.5 margin 后为负
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := baseCfg()
	cfg.GrabMinNetAnnualized = 4.0
	r := Evaluate(c, cfg, 0, 0, 0, 0)
	// gross 4.0 − margin 4.5 = −0.5 → 恒为 trap（不进任何视图）
	if r.NetBps >= 0 {
		t.Fatalf("NetBps = %v, want < 0（margin 未扣则净收益虚高）", r.NetBps)
	}
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap（net 4.0-4.5=-0.5 ≤ 0）", r.Rating)
	}
}

// TestEvaluateTrap 负净收益恒为 trap（不进任何视图）。
func TestEvaluateTrap(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "X",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		GrossBps:  1.0, // 1.0 - (0.6+2.0)*2 < 0
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.5, 0.5, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (net <= 0)", r.Rating)
	}
}

// TestEvaluateCrossVenueRoundTrip D-078：round-trip 成本验证——低 gross 机会被正确判 trap。
// 旧模型 gross=5 只扣 2.6 → net=2.4（grab）；round-trip 后扣 5.2 → net=-0.2（trap）。
func TestEvaluateCrossVenueRoundTrip(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		GrossBps:  5.0, // 5.0 - (0.6+2.0)*2 = -0.2 → trap
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.5, 0.5, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (gross 5bps < round-trip cost 5.2bps)", r.Rating)
	}
	// gross=10 → net = 10 - 5.2 = 4.8 → grab（round-trip 后仍盈利）
	c.GrossBps = 10.0
	r2 := Evaluate(c, baseCfg(), 0.5, 0.5, 0.6, 0)
	if r2.Rating != RatingGrab {
		t.Fatalf("Rating = %s, want grab (gross 10bps > round-trip cost 5.2bps)", r2.Rating)
	}
	if r2.NetBps < 4.7 || r2.NetBps > 4.9 {
		t.Fatalf("NetBps = %v, want ~4.8", r2.NetBps)
	}
}
