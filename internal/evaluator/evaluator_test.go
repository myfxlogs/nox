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
	// net = 8.18 - 0.6(佣金) - 1.0*2(滑点) = 5.58（不再减 0.5+0.5 点差）
	if r.NetBps < 5.5 || r.NetBps > 5.7 {
		t.Fatalf("NetBps = %v, want ~5.58", r.NetBps)
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
	// dailyNetBps = 5.5/100×10⁴/365 ≈ 1.507; oneTime = 1.0(点差)+0.6(佣金)+2.0(滑点) = 3.6; bd ≈ 2.39
	if *r.BreakevenDays < 2.3 || *r.BreakevenDays > 2.5 {
		t.Fatalf("BreakevenDays = %v, want ~2.39", *r.BreakevenDays)
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
		GrossBps:  1.0, // 1.0 - 0.6 - 2.0 < 0
		QuoteTime: time.Now().UnixMilli(),
	}
	r := Evaluate(c, baseCfg(), 0.5, 0.5, 0.6, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (net <= 0)", r.Rating)
	}
}
