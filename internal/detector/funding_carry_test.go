package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestFundingHedgeBaselineD-084 同所基准费率扣除口径：同所扣 baseline 后过门槛 / 跨所不扣直入。
func TestFundingHedgeBaseline(t *testing.T) {
	now := time.Now()
	ttl := 30 * time.Second
	const minNet = 4.0     // 门槛（Funding 档 MinNetAnnualized）
	const baseline = 10.95 // D-084 基准费率年化

	t.Run("同所基准不入（gross=10.95 → excess=0 < 4 不入库）", func(t *testing.T) {
		// okx:spot + okx:perp 同所，funding 恰等于基准 → 扣后 0 < 4 → 不产候选
		snapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
				"okx:perp": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		funding := map[string]map[string]float64{
			"BTCUSDT": {"okx": 10.95},
		}
		fp := &fakeFactProvider{funding: funding}
		d := &FundingHedgeDetector{MinNetAnnualized: minNet, BaselinePct: baseline, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 0 {
			t.Fatalf("同所 gross=baseline 应扣后 excess=0 < 4 不入库，实际 %d 候选", len(cands))
		}
	})

	t.Run("同所尖峰入库（gross=37 → excess=26.05 ≥ 4，GrossBps 保持原值 37）", func(t *testing.T) {
		snapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
				"okx:perp": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		funding := map[string]map[string]float64{
			"BTCUSDT": {"okx": 37.0},
		}
		fp := &fakeFactProvider{funding: funding}
		d := &FundingHedgeDetector{MinNetAnnualized: minNet, BaselinePct: baseline, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 1 {
			t.Fatalf("同所尖峰 37 应入库，实际 %d 候选", len(cands))
		}
		if cands[0].GrossBps != 37.0 {
			t.Errorf("GrossBps = %v, want 37.0（入库行保持原值，扣基准只用于门槛判定）", cands[0].GrossBps)
		}
		if cands[0].VenueBuy != "okx:spot" || cands[0].VenueSell != "okx:perp" {
			t.Errorf("VenueBuy/Sell = %s/%s, want okx:spot/okx:perp", cands[0].VenueBuy, cands[0].VenueSell)
		}
	})

	t.Run("跨所不扣直入（binance:spot + okx:perp，gross=5 ≥ 4 直接入库）", func(t *testing.T) {
		// 跨所差分本身已是超额口径，不扣 baseline；gross 5 ≥ 4 门槛 → 入库
		snapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"binance:spot": {Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
				"okx:perp":     {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		funding := map[string]map[string]float64{
			"BTCUSDT": {"okx": 5.0},
		}
		fp := &fakeFactProvider{funding: funding}
		d := &FundingHedgeDetector{MinNetAnnualized: minNet, BaselinePct: baseline, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 1 {
			t.Fatalf("跨所 gross=5 ≥ 4 应直入不扣，实际 %d 候选", len(cands))
		}
		if cands[0].GrossBps != 5.0 {
			t.Errorf("GrossBps = %v, want 5.0（跨所不扣基准保持原值）", cands[0].GrossBps)
		}
	})
}

// 确保 FundingHedgeDetector 实现 Detector 接口。
func TestFundingHedgeDetectorImplementsInterface(t *testing.T) {
	var _ Detector = (*FundingHedgeDetector)(nil)
}
