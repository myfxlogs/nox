package oppmatch

import (
	"context"
	"testing"

	"nox/internal/data/fact"
	"nox/internal/platform/testutil/storetest"
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
	// 种 index_price 跨所两行——cross_venue_spot 得正价差
	for _, f := range []fact.Fact{
		{Kind: "index_price", Venue: "binance", Symbol: "ABCUSDT", Value: 100.0},
		{Kind: "index_price", Venue: "gate", Symbol: "ABCUSDT", Value: 102.0},
	} {
		if err := st.InsertFacts(ctx, []fact.Fact{f}); err != nil {
			t.Fatalf("seed %s: %v", f.Venue, err)
		}
	}
	got, err := quoteFor(ctx, st, "cross_venue_spot")
	if err != nil {
		t.Fatalf("cross_venue_spot: %v", err)
	}
	if got == nil || *got <= 0.019 || *got > 0.021 {
		t.Fatalf("价差应≈0.02，实得 %v", got)
	}
}
