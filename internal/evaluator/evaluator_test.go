package evaluator

import (
	"testing"
	"time"

	"arb/internal/detector"
)

func TestEvaluateCrossVenue(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "EURUSD",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		PriceBuy:  1.1001,
		PriceSell: 1.1010,
		GrossBps:  8.18,
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := Config{
		SlippageBps:          1.0,
		MarginRateAnnual:     4.5,
		DetectorFreshnessTTL: 60 * time.Second,
		OpportunityTTL:       30 * time.Second,
	}
	r := Evaluate(c, cfg, 0.5, 0.5, 0.6, 0, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.Rating != RatingGrab {
		t.Fatalf("Rating = %s, want grab", r.Rating)
	}
	// net = 8.18 - 0.5 - 0.5 - 1.0*2 = 5.68
	if r.NetBps < 4.0 || r.NetBps > 5.0 {
		t.Fatalf("NetBps = %v, want ~4.58", r.NetBps)
	}
	if r.BreakevenDays != nil {
		t.Fatal("CrossVenue should not have breakeven_days")
	}
}

func TestEvaluateCarryBreakeven(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCarry,
		Canonical: "X",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		GrossBps:  10.0, // 年化 bps
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := Config{
		SlippageBps:      1.0,
		MarginRateAnnual: 4.5,
		OpportunityTTL:   30 * time.Second,
	}
	r := Evaluate(c, cfg, 0.5, 0.5, 0.6, 0, 0)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.BreakevenDays == nil {
		t.Fatal("Carry should have breakeven_days")
	}
	// oneTime = 0.5+0.5+1.0*2 = 3.0; dailyNet = 10/365 = 0.0274; bd = 3.0/0.0274 ≈ 109.5
	if *r.BreakevenDays < 120 || *r.BreakevenDays > 140 {
		t.Fatalf("BreakevenDays = %v, want ~131", *r.BreakevenDays)
	}
}

func TestEvaluateTrap(t *testing.T) {
	c := detector.Candidate{
		Type:      detector.TypeCrossVenue,
		Canonical: "X",
		VenueBuy:  "a:mt4",
		VenueSell: "b:mt4",
		GrossBps:  1.0,
		QuoteTime: time.Now().UnixMilli(),
	}
	cfg := Config{SlippageBps: 1.0, OpportunityTTL: 30 * time.Second}
	r := Evaluate(c, cfg, 0.5, 0.5, 0.6, 0, 0)
	if r.Rating != RatingTrap {
		t.Fatalf("Rating = %s, want trap (net <= 0)", r.Rating)
	}
}
