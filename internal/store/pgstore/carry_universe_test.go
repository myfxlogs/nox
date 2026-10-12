// D-345 测试：ReplaceCarryUniverse note 列落库回读（ suspect 门注记不丢）。
package pgstore

import (
	"context"
	"testing"
)

// carryUniverseTestStore 连接 ARB_TEST_PG_DSN 测试库，TRUNCATE fin_carry_universe 隔离数据。
func carryUniverseTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_carry_universe CASCADE"); err != nil {
		t.Fatalf("TRUNCATE fin_carry_universe: %v", err)
	}
	return s
}

// TestReplaceCarryUniverse_NoteRoundTrip D-345：note 写入→SQL 回读一致（红=INSERT 列缺 note）。
func ptrFloat(v float64) *float64 { return &v }

func TestReplaceCarryUniverse_NoteRoundTrip(t *testing.T) {
	s := carryUniverseTestStore(t)
	ctx := context.Background()
	dirty := 0.0
	if err := s.ReplaceCarryUniverse(ctx, []CarryUniverseRow{
		{Canonical: "DOT", MtVenue: "1314615", CexVenue: "binance", NetAnnualPct: 0,
			FundingAnnualPct: 12, SwapLongPct: ptrFloat(-469.5), SwapFree: false,
			Structure: "long_cfd_short_perp", Note: "suspect: swap 469.5%"},
		{Canonical: "BTC", MtVenue: "277842155", CexVenue: "binance", NetAnnualPct: 6.65,
			FundingAnnualPct: 6.65, SwapFree: true, Structure: "long_cfd_short_perp", Note: ""},
	}); err != nil {
		t.Fatalf("ReplaceCarryUniverse: %v", err)
	}
	type row struct {
		canonical, note string
		net             float64
	}
	rows, err := s.pool.Query(ctx, `SELECT canonical, note, net_annual_pct FROM fin_carry_universe ORDER BY canonical`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.canonical, &r.note, &r.net); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("rows=%d, want 2", len(got))
	}
	if got[0].canonical != "BTC" || got[0].note != "" || got[0].net != 6.65 {
		t.Errorf("BTC 行 = %+v, want note='' net=6.65", got[0])
	}
	if got[1].canonical != "DOT" || got[1].note != "suspect: swap 469.5%" || got[1].net != dirty {
		t.Errorf("DOT 行 = %+v, want note='suspect: swap 469.5%%' net=0", got[1])
	}
}

// TestListCfdUniverseDepthBases D-057 R1：CFD 域 universe 基名解析——
// equiv perp 行解析（SILVER@okx→XAGUSDT、BRENT@gate→BZUSDT）、无 equiv 用原值
// （XAU）、enabled=false 不解析（FOO）、CEX×CEX 行（mt_venue 哨兵 '(spot)'）不入。
func TestListCfdUniverseDepthBases(t *testing.T) {
	s := carryUniverseTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE asset_equiv RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE asset_equiv: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue, enabled) VALUES
		('SILVER','XAGUSDT','perp','okx',true),
		('BRENT','BZUSDT','perp','gate',true),
		('FOO','FOOXUSDT','perp','okx',false)`); err != nil {
		t.Fatalf("seed asset_equiv: %v", err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `TRUNCATE asset_equiv RESTART IDENTITY`) })
	if err := s.ReplaceCarryUniverse(ctx, []CarryUniverseRow{
		{Canonical: "SILVER", MtVenue: "3001157", CexVenue: "okx", Structure: "long_cfd_short_perp"},
		{Canonical: "BRENT", MtVenue: "3001157", CexVenue: "gate", Structure: "long_cfd_short_perp"},
		{Canonical: "XAU", MtVenue: "3001157", CexVenue: "okx", Structure: "long_cfd_short_perp"},
		{Canonical: "FOO", MtVenue: "3001157", CexVenue: "okx", Structure: "long_cfd_short_perp"},
		{Canonical: "MEME", MtVenue: "(spot)", CexVenue: "okx", Structure: "spot_carry"}, // CEX×CEX 哨兵行不入
	}); err != nil {
		t.Fatalf("ReplaceCarryUniverse: %v", err)
	}
	got, err := s.ListCfdUniverseDepthBases(ctx)
	if err != nil {
		t.Fatalf("ListCfdUniverseDepthBases: %v", err)
	}
	want := []string{"BZUSDT", "FOO", "XAGUSDT", "XAU"} // ORDER BY 1：BZ<F<X<X
	if len(got) != len(want) {
		t.Fatalf("bases = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bases[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
