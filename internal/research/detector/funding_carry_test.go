package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
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
		// 实施修正①：跨所 spot+perp 无差分，funding 恒=perp 腿单边 → 统一扣基准。
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

// TestFundingHedgeAllowFilter D-017 发现域收口：Allow 前置滤——白名单外组合静默
// 不发射（零候选），白名单内照常；nil=不过滤向后兼容。快照按 venue 对隔离构造
// （同 canonical 的 venues map 迭代只取一对 spot/perp，混布会引入迭代序随机性）。
func TestFundingHedgeAllowFilter(t *testing.T) {
	now := time.Now()
	funding := map[string]map[string]float64{"BTCUSDT": {"okx": 37.0, "gate": 37.0}}
	fp := &fakeFactProvider{funding: funding}
	okxOnly := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"okx:spot": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
			"okx:perp": {Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
		},
	}
	gateOnly := map[string]map[string]bus.Quote{
		"BTCUSDT": {
			"gate:spot": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "spot", Bid: 100000, Ask: 100010, Time: now},
			"gate:perp": {Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now},
		},
	}
	allowOKX := func(canonical, canonicalSell, venueBuy, venueSell string) bool {
		return venueBuy == "okx:spot" && venueSell == "okx:perp"
	}

	t.Run("nil=不过滤（既有装配向后兼容）", func(t *testing.T) {
		d := &FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second}
		if got := len(d.Scan(context.Background(), okxOnly, fp)); got != 1 {
			t.Fatalf("无 Allow 应 1 候选，实际 %d", got)
		}
	})
	t.Run("allowlist 内对正常发射", func(t *testing.T) {
		d := &FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second, Allow: allowOKX}
		cands := d.Scan(context.Background(), okxOnly, fp)
		if len(cands) != 1 || cands[0].VenueBuy != "okx:spot" || cands[0].VenueSell != "okx:perp" {
			t.Fatalf("应仅 okx 对 1 候选，实际 %+v", cands)
		}
	})
	t.Run("白名单外对静默零候选", func(t *testing.T) {
		d := &FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second, Allow: allowOKX}
		if got := len(d.Scan(context.Background(), gateOnly, fp)); got != 0 {
			t.Fatalf("gate 对不在 allowlist 应零候选，实际 %d", got)
		}
	})
	t.Run("空 allowlist=全拒（fail-closed 零候选）", func(t *testing.T) {
		d := &FundingHedgeDetector{MinNetAnnualized: 4.0, FreshnessTTL: 30 * time.Second,
			Allow: func(string, string, string, string) bool { return false }}
		if got := len(d.Scan(context.Background(), okxOnly, fp)); got != 0 {
			t.Fatalf("空白名单应零候选，实际 %d", got)
		}
	})
}
