package oppmatch

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestRegimeLabel 标签四型锚。
func TestRegimeLabel(t *testing.T) {
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
		if got := RegimeLabel(c.st); got != c.want {
			t.Errorf("%+v => %s want %s", c.st, got, c.want)
		}
	}
}

// TestComboBookTriple typed key 双腿→簿三元组键序锚。
func TestComboBookTriple(t *testing.T) {
	p, c, m := comboBookTriple(pgstore.XAssetAllowRow{
		CanonicalBuy: "BZUSDT", CanonicalSell: "BRENT",
		VenueBuy: "binance", VenueSell: "3001157:mt5"})
	if p != "BZUSDT-BRENT" || c != "binance:perp" || m != "3001157:mt5" {
		t.Fatalf("%s|%s|%s", p, c, m)
	}
	// MT 腿在 buy 位——pair 仍 cexSym-mtSym。
	p, c, m = comboBookTriple(pgstore.XAssetAllowRow{
		CanonicalBuy: "BRENT", CanonicalSell: "BZUSDT",
		VenueBuy: "3001157:mt5", VenueSell: "binance"})
	if p != "BZUSDT-BRENT" || c != "binance:perp" || m != "3001157:mt5" {
		t.Fatalf("%s|%s|%s", p, c, m)
	}
	var _ = time.Now
}
