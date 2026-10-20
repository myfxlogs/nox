package oppmatch

import (
	"context"
	"testing"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// TestQuoteForThreeTemplates D-637 序②：三模板报价源链走通+无源 fail-closed。
func TestQuoteForThreeTemplates(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	for _, tid := range []string{"cfd_swap_carry", "cross_venue_spot", "term_basis"} {
		if _, err := quoteFor(ctx, st, tid); err != nil {
			t.Fatalf("%s quote: %v", tid, err)
		}
	}
	// 种 fin_cex_spread 行——cross_venue_spot 得正价差（spread_bps/100=percent 口径）
	if err := st.InsertCexSpread(ctx, &pgstore.CexSpread{
		Symbol: "ABCUSDT", VenueBuy: "binance", VenueSell: "gate",
		PriceBuy: 100.0, PriceSell: 102.0, SpreadBps: 2.0,
	}); err != nil {
		t.Fatalf("seed spread: %v", err)
	}
	got, err := quoteFor(ctx, st, "cross_venue_spot")
	if err != nil {
		t.Fatalf("cross_venue_spot: %v", err)
	}
	if got == nil || *got <= 0.019 || *got > 0.021 {
		t.Fatalf("价差应≈0.02，实得 %v", got)
	}
}
