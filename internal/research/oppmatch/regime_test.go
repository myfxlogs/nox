package oppmatch

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestRegimeLabel 标签四型锚（判据=cfg 注入，禁硬编码）。
func TestRegimeLabel(t *testing.T) {
	cfg := regimeCfg{volBps: 8, trendBps: 6, minRows: 12, staleMax: 15 * time.Minute}
	cases := []struct {
		st   pgstore.XAssetSpreadStats
		want string
	}{
		{pgstore.XAssetSpreadStats{Rows: 5}, "illiquid"},
		{pgstore.XAssetSpreadStats{Rows: 20, Stdev: 9}, "volatile"},
		{pgstore.XAssetSpreadStats{Rows: 20, Stdev: 1, Delta: 7}, "trending"},
		{pgstore.XAssetSpreadStats{Rows: 20, Stdev: 1, Delta: 0}, "calm"},
	}
	for _, c := range cases {
		if got := cfg.RegimeLabel(c.st); got != c.want {
			t.Errorf("%+v => %s want %s", c.st, got, c.want)
		}
	}
}

// TestComboBookTriple typed key 双腿→簿三元组键序锚（venue_key 原值直用）。
func TestComboBookTriple(t *testing.T) {
	p, c, m, mc := comboBookTriple(pgstore.XAssetAllowRow{
		CanonicalBuy: "BZUSDT", CanonicalSell: "BRENT",
		VenueBuy: "binance:perp", VenueSell: "3001157:mt5"})
	if p != "BZUSDT-BRENT" || c != "binance:perp" || m != "3001157:mt5" || mc != "BRENT" {
		t.Fatalf("%s|%s|%s|%s", p, c, m, mc)
	}
	// MT 腿在 buy 位——pair 仍 cexSym-mtSym。
	p, c, m, mc = comboBookTriple(pgstore.XAssetAllowRow{
		CanonicalBuy: "BRENT", CanonicalSell: "BZUSDT",
		VenueBuy: "3001157:mt5", VenueSell: "binance:perp"})
	if p != "BZUSDT-BRENT" || c != "binance:perp" || m != "3001157:mt5" || mc != "BRENT" {
		t.Fatalf("%s|%s|%s|%s", p, c, m, mc)
	}
}
