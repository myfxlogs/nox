package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// fakeFactProvider 实现 FactProvider，注入 funding facts。
type fakeFactProvider struct {
	funding map[string]map[string]float64
}

func (f *fakeFactProvider) FundingFacts() map[string]map[string]float64 { return f.funding }
func (f *fakeFactProvider) SwapFacts() map[string]map[string][2]float64 { return nil }
func (f *fakeFactProvider) TradeModes() map[string]map[string]int64     { return nil }

func TestPerpFundingDetector(t *testing.T) {
	now := time.Now()
	ttl := 30 * time.Second

	// 两个 CEX 的 perp BTCUSDT 报价 + funding
	snapshot := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:perp":     {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp":    {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 99990, Ask: 100000, Time: now},
			"binance:spot": {Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
		},
	}
	funding := map[string]map[string]float64{
		"BTCUSDT": {
			"okx":     6.10,  // 高 funding → 持空收
			"gate":    -1.31, // 低 funding → 持多付
			"binance": 5.62,
		},
	}
	fp := &fakeFactProvider{funding: funding}

	t.Run("funding差超门槛→产候选", func(t *testing.T) {
		d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) == 0 {
			t.Fatalf("应产候选，实际 0")
		}
		// okx(6.10) vs gate(-1.31) = 7.41 差 → okx 持空、gate 持多
		found := false
		for _, c := range cands {
			if c.VenueBuy == "gate:perp" && c.VenueSell == "okx:perp" {
				found = true
				if c.GrossBps < 7.0 || c.GrossBps > 8.0 {
					t.Errorf("GrossBps = %.2f，应在 7~8（okx 6.10 - gate -1.31 = 7.41）", c.GrossBps)
				}
			}
		}
		if !found {
			t.Errorf("未找到 gate:perp→okx:perp 候选（低 funding 买、高 funding 卖）")
		}
	})

	t.Run("funding差低于门槛→不产", func(t *testing.T) {
		d := &PerpFundingDetector{MinNetAnnualized: 10.0, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		if len(cands) != 0 {
			t.Fatalf("门槛 10%% 应无候选，实际 %d", len(cands))
		}
	})

	t.Run("同所perp跳过", func(t *testing.T) {
		// 只有一个 perp venue → 无配对
		single := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"okx:perp": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
			},
		}
		d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), single, fp)
		if len(cands) != 0 {
			t.Fatalf("单 perp 应无候选，实际 %d", len(cands))
		}
	})

	t.Run("funding缺失跳过", func(t *testing.T) {
		fpNoFunding := &fakeFactProvider{funding: map[string]map[string]float64{}}
		d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fpNoFunding)
		if len(cands) != 0 {
			t.Fatalf("无 funding facts 应无候选，实际 %d", len(cands))
		}
	})

	t.Run("报价陈旧跳过", func(t *testing.T) {
		staleSnapshot := map[string]map[string]bus.Quote{
			"BTCUSDT": {
				"okx:perp":  {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now.Add(-60 * time.Second)},
				"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 99990, Ask: 100000, Time: now.Add(-60 * time.Second)},
			},
		}
		d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), staleSnapshot, fp)
		if len(cands) != 0 {
			t.Fatalf("陈旧报价应无候选，实际 %d", len(cands))
		}
	})

	t.Run("spot不参与", func(t *testing.T) {
		// snapshot 有 spot 但 detector 只看 perp
		d := &PerpFundingDetector{MinNetAnnualized: 0.01, FreshnessTTL: ttl}
		cands := d.Scan(context.Background(), snapshot, fp)
		for _, c := range cands {
			if c.VenueBuy == "binance:spot" || c.VenueSell == "binance:spot" {
				t.Errorf("spot 不应参与 perp funding 配对")
			}
		}
	})
}

// 确保 PerpFundingDetector 实现 Detector 接口
func TestPerpFundingDetectorImplementsInterface(t *testing.T) {
	var _ Detector = (*PerpFundingDetector)(nil)
}
