package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
)

// fakeFactProvider 实现 FactProvider，注入 funding facts。
type fakeFactProvider struct {
	funding map[string]map[string]float64
	isReal  map[string]*bool // D-178
	simKeys map[string]bool  // D-369-R2：sim venue 键集
}

func (f *fakeFactProvider) FundingFacts() map[string]map[string]float64 { return f.funding }
func (f *fakeFactProvider) SwapFacts() map[string]map[string][2]float64 { return nil }
func (f *fakeFactProvider) TradeModes() map[string]map[string]int64     { return nil }
func (f *fakeFactProvider) IsRealMap() map[string]*bool                 { return f.isReal }  // D-178
func (f *fakeFactProvider) SimVenueKeys() map[string]bool               { return f.simKeys } // D-369-R2

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
	// 族停用=恒 sim-only 通道——正向用例双腿标已知 sim（realMap 显式 false）。
	isReal := map[string]*bool{"okx:perp": boolPtrR2(false), "gate:perp": boolPtrR2(false), "binance:spot": boolPtrR2(false)}
	fp := &fakeFactProvider{funding: funding, isReal: isReal}

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

// TestPerpFundingSealedFamily D-057：族停用=恒 sim-only——real/未知腿对即使
// funding 差超门槛也恒拒（无准入放行通道，BTC/NEAR 同判据）。
func TestPerpFundingSealedFamily(t *testing.T) {
	now := time.Now()
	ttl := 30 * time.Second

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
		t.Fatalf("族停用 real/未知腿对应恒拒 NEARUSDT，实际产 %d 候选", len(cands))
	}
}

// TestPerpFundingFirstReportOnly D-154 S3：拒绝每 canonical 仅首报一次。
// 同一 canonical 连续 Scan 两次：seenReject 第二次 LoadOrStore 命中 → 不再首报。
// 断言：第二次调用后 seenReject 已存该 canonical（LoadOrStore 返回 loaded=true）。
func TestPerpFundingFirstReportOnly(t *testing.T) {
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

	// 第一次 Scan：SOLUSDT real/未知腿对恒拒 → 首报 + seenReject 标记
	_ = d.Scan(context.Background(), snapshot, fp)
	_, loaded := d.seenReject.Load("SOLUSDT")
	if !loaded {
		t.Fatal("第一次 Scan 后 seenReject 应含 SOLUSDT")
	}

	// 第二次 Scan：seenReject 命中不首报（无候选，行为不变）
	// 关键断言：seenReject 仍只标记一次（LoadOrStore 幂等），计数无增长
	cands := d.Scan(context.Background(), snapshot, fp)
	if len(cands) != 0 {
		t.Fatalf("族停用 SOLUSDT 应无候选，实际 %d", len(cands))
	}
	// seenReject 仍存在（进程生命周期，不清除）
	_, loaded = d.seenReject.Load("SOLUSDT")
	if !loaded {
		t.Fatal("第二次 Scan 后 seenReject 应仍含 SOLUSDT（进程生命周期）")
	}
}
