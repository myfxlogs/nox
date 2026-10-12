// d532_watchlist_test.go D-532 S1：watchlist provider venue 作用域过滤。
package main

import (
	"context"
	"testing"
)

// venues=[okx] 行进 okx 集不进 binance/gate；空 venues 行进全部；交集行精确归属。
func TestD532_WatchlistVenueFilter(t *testing.T) {
	s := d369Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE radar_watchlist`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, up := range []struct {
		sym    string
		venues []string
	}{
		{"ONLYOKX", []string{"okx"}},
		{"ALLV", []string{}},
		{"BG", []string{"binance", "gate"}},
	} {
		if err := s.UpsertRadarWatchlist(ctx, up.sym, up.venues, "divergence"); err != nil {
			t.Fatalf("upsert %s: %v", up.sym, err)
		}
	}
	w := newWatchlistSymbolProvider(ctx, s, nil)
	contains := func(set []string, sym string) bool {
		for _, x := range set {
			if x == sym {
				return true
			}
		}
		return false
	}
	bin := w.Symbols("binance")
	if contains(bin, "ONLYOKX") || !contains(bin, "ALLV") || !contains(bin, "BG") {
		t.Fatalf("binance 集应含 ALLV/BG 不含 ONLYOKX: %v", bin)
	}
	okx := w.Symbols("okx")
	if !contains(okx, "ONLYOKX") || !contains(okx, "ALLV") || contains(okx, "BG") {
		t.Fatalf("okx 集应含 ONLYOKX/ALLV 不含 BG: %v", okx)
	}
}
