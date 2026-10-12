// symdig_d464_test.go D-464：fin_symbol_aliases/fin_symdig_findings 读写面真库测。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestSymdigStore_D464(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, tbl := range []string{"fin_symdig_findings", "fin_carry_universe", "fin_symbol_aliases"} {
		if _, err := s.pool.Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	// R2：种子行已删——测内自插两行（GOLD→XAU + PLATINUM→XPT upsert）。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_symbol_aliases (alias,canonical,note) VALUES ('GOLD','XAU','d464test')`); err != nil {
		t.Fatal(err)
	}
	al, err := s.ListSymbolAliases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if al["GOLD"] != "XAU" {
		t.Fatalf("aliases=%v", al)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_symbol_aliases (alias,canonical,note) VALUES ('PLATINUM','XPT','d464test')
		 ON CONFLICT (alias) DO UPDATE SET canonical=EXCLUDED.canonical`); err != nil {
		t.Fatal(err)
	}
	al, _ = s.ListSymbolAliases(ctx)
	if al["PLATINUM"] != "XPT" {
		t.Fatalf("aliases=%v", al)
	}
	// universe pairs
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_carry_universe (canonical,mt_venue,cex_venue,net_annual_pct,funding_annual_pct,swap_free,structure,note)
		 VALUES ('XAU','3001157','gate:perp',5,4,true,'long_cfd_short_perp','')`); err != nil {
		t.Fatal(err)
	}
	pairs, err := s.ListCarryUniversePairs(ctx)
	if err != nil || !pairs["3001157|XAU"] {
		t.Fatalf("pairs=%v err=%v", pairs, err)
	}
	// findings 写+上轮键集
	runTS := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	err = s.InsertSymdigFindings(ctx, runTS, []SymdigFinding{
		{Venue: "v1", Symbol: "GOLD", Klass: "unmapped_unknown", Reason: "unmapped", Payload: map[string]any{"trade_mode": 4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := s.LatestSymdigKeys(ctx)
	if err != nil || !keys["v1|GOLD|unmapped_unknown"] {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}
