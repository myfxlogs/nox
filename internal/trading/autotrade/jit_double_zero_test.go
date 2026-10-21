// jit_double_zero_test.go —— D-649 返修锚：同池双腿皆零时 JitSweepFn 须以
// 票面名义锚 venue 粒度单次调达（0−0=0 双跳过病灶修——combo#25 实证）。
package autotrade

import (
	"context"

	"nox/internal/platform/testutil"
	"testing"
	"time"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

type jitDblSvc struct {
	engineService
	eq map[string]float64
}

func (s *jitDblSvc) AccountEquityUSDAt(vk string) (float64, string, time.Time, bool) {
	return s.eq[vk], "USDT", time.Now(), true
}
func (s *jitDblSvc) RefreshAccountEquity(_ context.Context, vk string) { s.eq[vk] = 1500 }

// 双零同池+票面锚在场 → 单次扫（vk=pool venue，need=票面锚，oppAPR=NetBps/1e4）
// →权益重读>0→sizing 放行。
func TestJitDoubleZero_SingleSweepNotionalAnchor(t *testing.T) {
	var gotVK string
	var gotNeed, gotAPR float64
	calls := 0
	e := &Engine{service: &jitDblSvc{eq: map[string]float64{"okx:perp": 0}}}
	e.JitSweepFn = func(_ context.Context, vk string, need, apr float64) (string, error) {
		calls++
		gotVK, gotNeed, gotAPR = vk, need, apr
		return "", nil
	}
	spec := &exec.XAssetComboAllowSpec{
		VenueBuy: "okx:perp", VenueSell: "okx:perp",
		SizingMode: "pct", FundingPct: f64p(0.5), MaxGrossUSD: 1500,
	}
	opp := pgstore.Opportunity{ID: "o1", NetBps: 1470}
	if !e.resolveXAssetSizing(context.Background(), opp, spec) {
		t.Fatal("sizing 应放行")
	}
	if calls != 1 || gotVK != "okx:perp" || gotNeed != 1500 || gotAPR != 0.147 {
		t.Fatalf("归集调用错: n=%d vk=%s need=%.0f apr=%.4f", calls, gotVK, gotNeed, gotAPR)
	}
}

// NotionalUSD 优先于 MaxGrossUSD（票面名义锚序）。
func TestJitDoubleZero_NotionalFirst(t *testing.T) {
	var need float64
	e := &Engine{service: &jitDblSvc{eq: map[string]float64{"okx:perp": 0}}}
	e.JitSweepFn = func(_ context.Context, _ string, n, _ float64) (string, error) { need = n; return "", nil }
	spec := &exec.XAssetComboAllowSpec{
		VenueBuy: "okx:perp", VenueSell: "okx:perp",
		SizingMode: "pct", FundingPct: f64p(0.5), MaxGrossUSD: 1500, NotionalUSD: f64p(1200),
	}
	_ = e.resolveXAssetSizing(context.Background(), pgstore.Opportunity{ID: "o1", NetBps: 1470}, spec)
	if need != 1200 {
		t.Fatalf("锚序错 need=%.0f≠1200", need)
	}
}

// 异 venue 键双零 → 不触双零径（逐腿循环 0−0 跳过语义保），落箱面不扫。
func TestJitDoubleZero_DiffKeysNoSweep(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := &Engine{store: st, service: &jitDblSvc{eq: map[string]float64{"okx:perp": 0, "gate:perp": 0}}}
	e.JitSweepFn = func(_ context.Context, _ string, _, _ float64) (string, error) { calls++; return "", nil }
	spec := &exec.XAssetComboAllowSpec{
		VenueBuy: "okx:perp", VenueSell: "gate:perp",
		SizingMode: "pct", FundingPct: f64p(0.5), MaxGrossUSD: 1500,
	}
	if e.resolveXAssetSizing(context.Background(), pgstore.Opportunity{ID: "o1"}, spec) {
		t.Fatal("双零异池应拒开")
	}
	if calls != 0 {
		t.Fatalf("异池双零误触归集 %d 次", calls)
	}
}

func f64p(v float64) *float64 { return &v }
