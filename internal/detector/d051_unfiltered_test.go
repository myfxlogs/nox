// d051_unfiltered_test.go —— D-051①锚：ScanUnfiltered=Allow 前候选流——白名单拒绝
// 与净阈下潜（10~14.95 证据带）的候选仍发射；陈旧报价/sim 腿/funding 缺席仍剔除
// （证据有效性前提不绕）。
package detector

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

func d051Snapshot(now time.Time) map[string]map[string]bus.Quote {
	return map[string]map[string]bus.Quote{
		"AAAUSDT": {
			"okx:spot": {Symbol: "AAAUSDT", Venue: "okx", VenueType: "spot", Bid: 100, Ask: 100.1, Time: now},
			"okx:perp": {Symbol: "AAAUSDT", Venue: "okx", VenueType: "perp", Bid: 100, Ask: 100.1, Time: now},
		},
	}
}

// TestD051_ScanUnfiltered_BypassAllowAndNet 白名单拒+净阈下潜（funding=12 < 14.95
// 有效阈）候选：Scan 零产出 / ScanUnfiltered 发射（纸面闸所需证据流）。
func TestD051_ScanUnfiltered_BypassAllowAndNet(t *testing.T) {
	now := time.Now()
	fp := &fakeFactProvider{funding: map[string]map[string]float64{"AAAUSDT": {"okx": 12.0}}}
	d := &FundingHedgeDetector{
		MinNetAnnualized: 4.0, BaselinePct: 10.95, FreshnessTTL: time.Minute,
		Allow: func(_, _, _, _ string) bool { return false }, // 白名单全拒
	}
	if got := d.Scan(context.Background(), d051Snapshot(now), fp); len(got) != 0 {
		t.Fatalf("Scan（执行链）应零候选，got %d", len(got))
	}
	got := d.ScanUnfiltered(context.Background(), d051Snapshot(now), fp)
	if len(got) != 1 {
		t.Fatalf("ScanUnfiltered 应发射 Allow 前候选，got %d", len(got))
	}
	if got[0].Type != TypeFundingHedge || got[0].Canonical != "AAAUSDT" || got[0].GrossBps != 12.0 {
		t.Fatalf("候选 = %+v, want FUNDING_HEDGE/AAAUSDT/gross 12", got[0])
	}
}

// TestD051_ScanUnfiltered_KeepValidityGates unfiltered 仍剔：陈旧报价/混合
// is_real 腿/funding 事实缺席（仅白名单与净阈绕行）。
func TestD051_ScanUnfiltered_KeepValidityGates(t *testing.T) {
	now := time.Now()
	stale := map[string]map[string]bus.Quote{
		"AAAUSDT": {
			"okx:spot": {Symbol: "AAAUSDT", Venue: "okx", VenueType: "spot", Bid: 100, Ask: 100.1, Time: now.Add(-2 * time.Hour)},
			"okx:perp": {Symbol: "AAAUSDT", Venue: "okx", VenueType: "perp", Bid: 100, Ask: 100.1, Time: now},
		},
	}
	d := &FundingHedgeDetector{FreshnessTTL: time.Minute}
	fund := map[string]map[string]float64{"AAAUSDT": {"okx": 12.0}}

	// 陈旧 spot 腿 → 剔除
	fp := &fakeFactProvider{funding: fund}
	if got := d.ScanUnfiltered(context.Background(), stale, fp); len(got) != 0 {
		t.Fatalf("陈旧报价 unfiltered 仍应剔除，got %d", len(got))
	}
	// funding 事实缺席 → 剔除（无 funding 观测=非 FUNDING_HEDGE 候选）
	if got := d.ScanUnfiltered(context.Background(), d051Snapshot(now), &fakeFactProvider{}); len(got) != 0 {
		t.Fatalf("funding 缺席 unfiltered 仍应剔除，got %d", len(got))
	}
	// 混合 real/sim 腿（isRealPair 不一致）→ 剔除
	tr, f := true, false
	fp2 := &fakeFactProvider{funding: fund, isReal: map[string]*bool{"okx:spot": &tr, "okx:perp": &f}}
	if got := d.ScanUnfiltered(context.Background(), d051Snapshot(now), fp2); len(got) != 0 {
		t.Fatalf("混合 real/sim 腿 unfiltered 仍应剔除，got %d", len(got))
	}
}
