package main

// D-311 R2 锚：pipelineCSResolver 复合分派——:perp→fin_cex_universe.ct_val
// （命中=真值/缺行/NULL→ok=false fail-closed）；:spot→1.0；:mt4/:mt5→broker_symbol_map。

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestPipelineCSResolver_Dispatch(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_cex_universe, broker_symbol_map CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctVal := 0.01
	if err := st.UpsertCexUniverse(ctx, []pgstore.CexUniverseRow{
		{Venue: "gate", Symbol: "CL_USDT", Base: "CL", Class: "ENERGY", Status: "TRADING",
			FirstSeen: time.Now(), HedgeCanonical: "WTIUSD", Tier: "T1", CtVal: &ctVal},
		// ct_val NULL 行：缺值 ≠ 缺行，同样 fail-closed
		{Venue: "okx", Symbol: "BTC-USDT-SWAP", Base: "BTC", Class: "CRYPTO", Status: "TRADING",
			FirstSeen: time.Now(), HedgeCanonical: "", Tier: "T1"},
	}, time.Now()); err != nil {
		t.Fatalf("seed cex universe: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('3001157','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatalf("seed broker account: %v", err)
	}
	csMT := 100.0
	if err := st.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
		Venue: "3001157", BrokerSymbol: "WTIUSD", Canonical: "WTIUSD", ContractSize: &csMT,
	}); err != nil {
		t.Fatalf("seed symbol mapping: %v", err)
	}

	r := pipelineCSResolver(st)
	for _, tc := range []struct {
		name             string
		venueKey, symbol string
		wantCS           float64
		wantOK           bool
	}{
		{"perp ct_val hit", "gate:perp", "CL_USDT", 0.01, true},
		{"perp ct_val NULL", "okx:perp", "BTC-USDT-SWAP", 0, false},
		{"perp row missing", "binance:perp", "NOPEUSDT", 0, false},
		{"spot const 1.0", "gate:spot", "CL_USDT", 1.0, true},
		{"mt5 broker_symbol_map hit", "3001157:mt5", "WTIUSD", 100, true},
		{"mt5 mapping missing", "3001157:mt5", "EURUSD", 0, false},
	} {
		cs, ok := r(tc.venueKey, tc.symbol)
		if ok != tc.wantOK || (ok && cs != tc.wantCS) {
			t.Errorf("%s: ContractSize(%q,%q)=(%v,%v), want (%v,%v)",
				tc.name, tc.venueKey, tc.symbol, cs, ok, tc.wantCS, tc.wantOK)
		}
	}
}
