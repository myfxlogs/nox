// d544_tick_p95_test.go —— D-544 S3：非 mt×cex 结构 ticks 簿锚测试锚（先红后绿）。
// T1 spot_carry tick 对齐→B 级；T2 空集→D 级新文案；T3 mt×cex 仍走 monitor（回归锚）；
// T4 perp_perp 同走 tick 路径。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// d544SpotFixture spot_carry 档案底料：binance spot+perp 双腿同 symbol SKLUSDT。
func d544SpotFixture() *fakeGradDB {
	return &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 1, PaperID: 13, VenueA: "binance", VenueB: "binance",
			Symbol: "SKLUSDT", Structure: "spot_carry", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 13, VenueA: "binance", VenueB: "binance", Symbol: "SKLUSDT",
			Structure: "spot_carry", Side: "short_perp", QtyUSD: 1000, Status: "active",
			AccruedUSD: 2, EntryTs: time.Now().Add(-3 * 24 * time.Hour),
			EntryBasisBps: &[]float64{-36.9}[0]},
		ticks: map[string]pgstore.TickRow{
			"binance|spot|SKLUSDT": {Ts: time.Now(), Bid: 10.00, Ask: 10.02},
			"binance|perp|SKLUSDT": {Ts: time.Now(), Bid: 10.00, Ask: 10.02},
		},
	}
}

// TestD544_SpotCarryTickP95_BGrade T1（核心锚）：双腿对齐样本→AdverseBasisP95Usd
// 非 nil+B 级+gaps 无「非 mt×cex」文案。
func TestD544_SpotCarryTickP95_BGrade(t *testing.T) {
	f := d544SpotFixture()
	f.tickP95 = map[string]float64{
		// short_perp：perp 腿=SELL、spot 腿=BUY（gradRiskLegs Side 翻转）→ buy=spot
		"binance:spot|SKLUSDT|binance:perp|SKLUSDT": 39.29,
	}
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.AdverseBasisP95Usd == nil {
		t.Fatal("tick 对齐→AdverseBasisP95Usd 应非 nil")
	}
	if g := egOf(rep, "adverse_basis_p95_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_B {
		t.Errorf("grade=%v want B", g)
	}
	for _, gap := range rep.Gaps {
		if strings.Contains(gap, "非 mt×cex") {
			t.Errorf("B 级后不得再报旧文案：%s", gap)
		}
	}
}

// TestD544_TickEmpty_StillDGap T2：ticks 空集→D 级+新文案（诚实降级不破）。
func TestD544_TickEmpty_StillDGap(t *testing.T) {
	f := d544SpotFixture() // tickP95 未置=空集
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.AdverseBasisP95Usd != nil {
		t.Fatal("空集→应 NULL")
	}
	if g := egOf(rep, "adverse_basis_p95_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_D {
		t.Errorf("grade=%v want D", g)
	}
	want := "ticks 簿无 binance:spot×binance:perp 对齐样本（p95 缺）"
	found := false
	for _, gap := range rep.Gaps {
		if strings.Contains(gap, want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺新文案 %q：%v", want, rep.Gaps)
	}
}

// TestD544_MtCexStillMonitorPath T3 回归锚：mt×cex（cfd）结构仍走 monitor 簿
// （monitorP95Calls=1、tickP95Calls=0）——不混源。
func TestD544_MtCexStillMonitorPath(t *testing.T) {
	f := d471Fixture()
	f.monitorP95Calls, f.tickP95Calls = 0, 0
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if f.monitorP95Calls != 1 || f.tickP95Calls != 0 {
		t.Fatalf("mt×cex 应走 monitor：monitor=%d tick=%d", f.monitorP95Calls, f.tickP95Calls)
	}
	if rep.AdverseBasisP95Usd == nil {
		t.Fatal("monitor 命中→应非 nil")
	}
}

// TestD544_PerpPerpTickPath T4：perp_perp 双腿→同走 tick 路径。
func TestD544_PerpPerpTickPath(t *testing.T) {
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 2, PaperID: 14, VenueA: "binance", VenueB: "okx",
			Symbol: "BTC", Structure: "perp_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 14, VenueA: "binance", VenueB: "okx", Symbol: "BTC",
			Structure: "perp_perp", QtyUSD: 1000, Status: "active",
			AccruedUSD: 1, EntryTs: time.Now().Add(-2 * 24 * time.Hour)},
		ticks: map[string]pgstore.TickRow{
			"binance|perp|BTCUSDT":   {Ts: time.Now(), Bid: 100, Ask: 100.1},
			"okx|perp|BTC-USDT-SWAP": {Ts: time.Now(), Bid: 100, Ask: 100.1},
		},
		universe: map[string]pgstore.CexUniverseRow{
			"binance|BTCUSDT":   {Venue: "binance", Symbol: "BTCUSDT", HedgeCanonical: "BTC"},
			"okx|BTC-USDT-SWAP": {Venue: "okx", Symbol: "BTC-USDT-SWAP", HedgeCanonical: "BTC"},
		},
		tickP95: map[string]float64{
			// gradRiskLegs perp_perp：VenueB=buy（okx:perp）×VenueA=sell（binance:perp）
			"okx:perp|BTC-USDT-SWAP|binance:perp|BTCUSDT": 12.5,
		},
	}
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.AdverseBasisP95Usd == nil {
		t.Fatalf("perp_perp tick 对齐→应非 nil；gaps=%v", rep.Gaps)
	}
	if g := egOf(rep, "adverse_basis_p95_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_B {
		t.Errorf("grade=%v want B", g)
	}
}
