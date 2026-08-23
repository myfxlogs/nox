package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// TestFundingHedgeBaseline D-084 基准费率超额口径（实施修正①：跨所同所统一扣基准）。
// spot+perp 结构无差分（收益恒=perp 腿 venue 的 funding），跨所同所过同一超额尺子。
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

	t.Run("跨所基准不入（binance:spot + okx:perp，okx funding=10.95 → 扣后 0 < 4 不入库）", func(t *testing.T) {
		// D-084 实施修正①：跨所 spot+perp 无差分，funding 恒=perp 腿单边 → 统一扣基准。
		// 业主抓出的 binance:spot+okx:perp gross=10.95=基准 漏推场景，修正后应被挡。
		snapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"binance:spot": {Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
				"okx:perp":     {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		funding := map[string]map[string]float64{
			"BTCUSDT": {"okx": 10.95},
		}
		fp := &fakeFactProvider{funding: funding}
		d := &FundingHedgeDetector{MinNetAnnualized: minNet, BaselinePct: baseline, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 0 {
			t.Fatalf("跨所 gross=baseline 应统一扣后 excess=0 < 4 不入库，实际 %d 候选", len(cands))
		}
	})

	t.Run("跨所尖峰入库（binance:spot + okx:perp，okx funding=25 → excess=14.05 ≥ 4 入库，GrossBps=25）", func(t *testing.T) {
		// 跨所保留发射（库存币顺手套保场景：币在 A 所、B 所尖峰时跨所对冲），过同一超额尺子。
		snapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"binance:spot": {Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
				"okx:perp":     {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		funding := map[string]map[string]float64{
			"BTCUSDT": {"okx": 25.0},
		}
		fp := &fakeFactProvider{funding: funding}
		d := &FundingHedgeDetector{MinNetAnnualized: minNet, BaselinePct: baseline, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 1 {
			t.Fatalf("跨所尖峰 25 应入库（excess=14.05 ≥ 4），实际 %d 候选", len(cands))
		}
		if cands[0].GrossBps != 25.0 {
			t.Errorf("GrossBps = %v, want 25.0（入库行保持原值，扣基准只用于门槛判定）", cands[0].GrossBps)
		}
		if cands[0].VenueBuy != "binance:spot" || cands[0].VenueSell != "okx:perp" {
			t.Errorf("VenueBuy/Sell = %s/%s, want binance:spot/okx:perp", cands[0].VenueBuy, cands[0].VenueSell)
		}
	})
}

// 确保 FundingHedgeDetector 实现 Detector 接口。
func TestFundingHedgeDetectorImplementsInterface(t *testing.T) {
	var _ Detector = (*FundingHedgeDetector)(nil)
}
