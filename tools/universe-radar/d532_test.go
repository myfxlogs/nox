// d532_test.go D-532 S2：band_with_leg watchlist 写侧=canonical 基名（非 venue 原生形态）。
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func TestD532_BandWithLegWritesCanonical(t *testing.T) {
	st := newFakeRadarStore()
	ann := 999.0
	rows := []normRow{{
		Row:            Row{Symbol: "BZ-USDT-SWAP", Vol24h: 1e9},
		Venue:          "okx",
		Canonical:      "BZ",
		HedgeCanonical: "BTC", // 有腿 → 走 band_with_leg upsert
		FundingAnn:     &ann,
	}}
	var lines []string
	var touches []alertTouch
	d369RouteFundingEvents(context.Background(), st, rows,
		map[string]pgstore.CexUniverseRow{}, nil, time.Now(),
		50, 1e6, &lines,
		map[string]map[string]pgstore.RadarDigestMember{}, map[string][]string{},
		map[string][]string{}, &touches)
	found := false
	for _, u := range st.watchUpserts {
		if u == "BZ|okx|band_with_leg" {
			found = true
		}
		if strings.HasPrefix(u, "BZ-USDT-SWAP|") {
			t.Fatalf("写侧应为 canonical 非原生形态: %v", st.watchUpserts)
		}
	}
	if !found {
		t.Fatalf("应 upsert BZ|okx|band_with_leg, got %v", st.watchUpserts)
	}
}
