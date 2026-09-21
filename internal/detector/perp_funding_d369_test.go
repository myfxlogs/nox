// perp_funding_d369_test.go —— D-369 S3/S5：sim 试单通道锚。
package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestPerpFunding_SimPairBypass D-369 S3：非白名单 canonical（ONDO）双腿均已知 sim →
// 产候选；一腿 real → 零候选（fail-closed）。
func TestPerpFunding_SimPairBypass(t *testing.T) {
	now := time.Now()
	funding := map[string]map[string]float64{
		"ONDOUSDT": {"binance": 11.0, "gate": -16.2},
	}
	snapshot := map[string]map[string]bus.Quote{
		"ONDOUSDT": {
			"binance:perp": {Symbol: "ONDOUSDT", Venue: "binance", VenueType: "perp", Bid: 1.0, Ask: 1.001, Time: now},
			"gate:perp":    {Symbol: "ONDOUSDT", Venue: "gate", VenueType: "perp", Bid: 0.99, Ask: 1.0, Time: now},
		},
	}
	f := func(b bool) *bool { return &b }

	// 双腿已知 sim → 产候选
	fp := &fakeFactProvider{funding: funding, isReal: map[string]*bool{
		"binance": f(false), "gate": f(false),
	}}
	d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}
	cands := d.Scan(context.Background(), snapshot, fp)
	if len(cands) != 1 {
		t.Fatalf("sim×sim 候选 = %d, want 1（D-369 S3 通道）: %+v", len(cands), cands)
	}
	if cands[0].Canonical != "ONDOUSDT" {
		t.Errorf("canonical = %q, want ONDOUSDT", cands[0].Canonical)
	}

	// 一腿 real（混合）→ 零候选（fail-closed）
	fp2 := &fakeFactProvider{funding: funding, isReal: map[string]*bool{
		"binance": f(true), "gate": f(false),
	}}
	if cands := d.Scan(context.Background(), snapshot, fp2); len(cands) != 0 {
		t.Errorf("混合腿应零候选, got %d", len(cands))
	}

	// 未知（nil）不放行（fail-closed 语义：nil ≠ 已知 sim）
	fp3 := &fakeFactProvider{funding: funding, isReal: map[string]*bool{}}
	if cands := d.Scan(context.Background(), snapshot, fp3); len(cands) != 0 {
		t.Errorf("未知 realness 应零候选（fail-closed）, got %d", len(cands))
	}

	// 白名单 canonical（BTC）行为不变：real 混合 fail-open 照产
	btcSnapshot := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:perp":  {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 99990, Ask: 100000, Time: now},
		},
	}
	btcFunding := map[string]map[string]float64{"BTCUSDT": {"okx": 6.1, "gate": -1.31}}
	fp4 := &fakeFactProvider{funding: btcFunding, isReal: map[string]*bool{"gate": f(false)}}
	d4 := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}
	if cands := d4.Scan(context.Background(), btcSnapshot, fp4); len(cands) == 0 {
		t.Error("白名单 canonical（BTC）行为应完全不变（照产候选）")
	}
}
