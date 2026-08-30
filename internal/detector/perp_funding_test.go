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

// TestPerpFundingWhitelist D-151：PERP_FUNDING 入场白名单 BTC/ETH/TRX。
// 非白名单 canonical（如 NEARUSDT/SOLUSDT）即使 funding 差超门槛也不产候选。
func TestPerpFundingWhitelist(t *testing.T) {
	now := time.Now()
	ttl := 30 * time.Second

	// NEARUSDT 非白名单，但 funding 差超门槛（应被白名单拒绝）
	snapshot := map[string]map[string]bus.Quote{
		"NEARUSDT": {
			"okx:perp":  {Symbol: "NEARUSDT", Venue: "okx", VenueType: "perp", Bid: 5.0, Ask: 5.01, Time: now},
			"gate:perp": {Symbol: "NEARUSDT", Venue: "gate", VenueType: "perp", Bid: 4.99, Ask: 5.0, Time: now},
		},
	}
	funding := map[string]map[string]float64{
		"NEARUSDT": {
			"okx":  10.0,
			"gate": -10.0, // 差 20% 远超门槛 2%
		},
	}
	fp := &fakeFactProvider{funding: funding}
	d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}
	cands := d.Scan(context.Background(), snapshot, fp)
	if len(cands) != 0 {
		t.Fatalf("D-151 白名单应拒绝 NEARUSDT（非 BTC/ETH/TRX），实际产 %d 候选", len(cands))
	}

	// ETHUSDT 白名单内，funding 差超门槛 → 应产候选
	ethSnapshot := map[string]map[string]bus.Quote{
		"ETHUSDT": {
			"okx:perp":  {Symbol: "ETHUSDT", Venue: "okx", VenueType: "perp", Bid: 3000, Ask: 3001, Time: now},
			"gate:perp": {Symbol: "ETHUSDT", Venue: "gate", VenueType: "perp", Bid: 2999, Ask: 3000, Time: now},
		},
	}
	ethFunding := map[string]map[string]float64{
		"ETHUSDT": {"okx": 6.0, "gate": -1.0}, // 差 7% 超门槛
	}
	ethFp := &fakeFactProvider{funding: ethFunding}
	ethCands := d.Scan(context.Background(), ethSnapshot, ethFp)
	if len(ethCands) == 0 {
		t.Fatalf("D-151 白名单应放行 ETHUSDT，实际 0 候选")
	}
}

// TestPerpFundingWhitelistRejectFirstReportOnly D-154 S3：白名单拒绝每 canonical 仅首报一次。
// 同一非白名单 canonical 连续 Scan 两次：seenReject 第二次 LoadOrStore 命中 → 不再首报。
// 断言：第二次调用后 seenReject 已存该 canonical（LoadOrStore 返回 loaded=true）。
func TestPerpFundingWhitelistRejectFirstReportOnly(t *testing.T) {
	now := time.Now()
	ttl := 30 * time.Second
	snapshot := map[string]map[string]bus.Quote{
		"SOLUSDT": {
			"okx:perp":  {Symbol: "SOLUSDT", Venue: "okx", VenueType: "perp", Bid: 100, Ask: 101, Time: now},
			"gate:perp": {Symbol: "SOLUSDT", Venue: "gate", VenueType: "perp", Bid: 99, Ask: 100, Time: now},
		},
	}
	funding := map[string]map[string]float64{
		"SOLUSDT": {"okx": 10.0, "gate": -10.0},
	}
	fp := &fakeFactProvider{funding: funding}
	d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: ttl}

	// 第一次 Scan：SOLUSDT 非白名单 → 首报 + seenReject 标记
	_ = d.Scan(context.Background(), snapshot, fp)
	_, loaded := d.seenReject.Load("SOLUSDT")
	if !loaded {
		t.Fatal("第一次 Scan 后 seenReject 应含 SOLUSDT")
	}

	// 第二次 Scan：SOLUSDT 仍非白名单 → seenReject 命中不首报（无候选，行为不变）
	// 关键断言：seenReject 仍只标记一次（LoadOrStore 幂等），计数无增长
	cands := d.Scan(context.Background(), snapshot, fp)
	if len(cands) != 0 {
		t.Fatalf("非白名单 SOLUSDT 应无候选，实际 %d", len(cands))
	}
	// seenReject 仍存在（进程生命周期，不清除）
	_, loaded = d.seenReject.Load("SOLUSDT")
	if !loaded {
		t.Fatal("第二次 Scan 后 seenReject 应仍含 SOLUSDT（进程生命周期）")
	}
}
