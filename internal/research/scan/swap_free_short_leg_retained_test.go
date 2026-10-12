// 锚：collectMtSwaps swap-free 掩码次序——规格符号过滤须在 val=0 归零之后，
// 否则免息账户负 swap_short 油腿被当付息腿剔除（3001157 UKOILm/USOILm 实拍
// swap_short −16～−25 → short_cfd_long_perp 行缺失）。非免息回归不变。
package scan

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// d049Store BRENT×BZUSDT(gate) 场景：UKOILm 规格 swap_long=0 / swap_short=−16，
// gate BZUSDT funding=−22.8（生产实拍值）。
func d049Store(swapFree bool) *fakeScanStore {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "gate", Symbol: "BZUSDT", Value: -22.8, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "UKOILm", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "UKOILm", Value: -16, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "UKOILm", Canonical: "BRENT", IsPrimary: true},
		},
		equiv: []pgstore.AssetEquivEntry{
			{AssetClass: "BRENT", Canonical: "BRENT", VenueType: "mt5", Venue: "3001157"},
			{AssetClass: "BRENT", Canonical: "BZUSDT", VenueType: "perp", Venue: "gate"},
		},
	}
	if swapFree {
		st.swapFree = map[string]bool{"3001157": true}
	}
	return st
}

func d049Nets(t *testing.T, st *fakeScanStore) map[string]float64 {
	t.Helper()
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	nets := map[string]float64{}
	for _, r := range res.Rows {
		if r.Canonical == "BRENT" {
			nets[r.Structure] = r.NetAnnual
		}
	}
	return nets
}

// TestD049_SwapFreeShortLegRetained 免息账户 + 负规格 swap_short → 短腿保留
// val=0 → short_cfd_long_perp 行出 net=−funding=+22.8（D-049 验收锚）。
func TestD049_SwapFreeShortLegRetained(t *testing.T) {
	nets := d049Nets(t, d049Store(true))
	if len(nets) != 2 {
		t.Fatalf("免息账户应出双结构行，got %v", nets)
	}
	if nets["short_cfd_long_perp"] < 22.7 || nets["short_cfd_long_perp"] > 22.9 {
		t.Errorf("short net want ≈+22.8(=−funding), got %.4f", nets["short_cfd_long_perp"])
	}
	if nets["long_cfd_short_perp"] < -22.9 || nets["long_cfd_short_perp"] > -22.7 {
		t.Errorf("long net want ≈−22.8(=+funding), got %.4f", nets["long_cfd_short_perp"])
	}
}

// TestD049_NonSwapFreeRegression 非免息账户负规格 swap_short 仍剔除（回归）：
// 只剩 long_cfd_short_perp 行（swap_long=0 免息腿保留），short 行缺席。
func TestD049_NonSwapFreeRegression(t *testing.T) {
	nets := d049Nets(t, d049Store(false))
	if _, ok := nets["short_cfd_long_perp"]; ok {
		t.Errorf("非免息账户负 swap_short 应剔除 short 行，got %v", nets)
	}
	if nets["long_cfd_short_perp"] < -22.9 || nets["long_cfd_short_perp"] > -22.7 {
		t.Errorf("long net want ≈−22.8, got %.4f", nets["long_cfd_short_perp"])
	}
}
