// venue_account_snapshot_test.go —— D-358 S4：InsertVenueAccountSnapshots 批插锚
// （复用 testStore 全量 Migrate，顺带验证 0111 migration 可应用）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestInsertVenueAccountSnapshots D-358 S4：批量插入→回读行数与原币字段；空切片 no-op。
func TestInsertVenueAccountSnapshots(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_venue_account_snapshot"); err != nil {
		t.Fatalf("TRUNCATE fin_venue_account_snapshot: %v", err)
	}
	ts := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		{Venue: "binance", VenueType: "perp", BalanceRaw: 100.5, EquityRaw: 101.5,
			FreeMarginRaw: 90, Currency: "USDT", FetchedAt: ts},
		{Venue: "Exness", VenueType: "mt5", BalanceRaw: 10000, EquityRaw: 10050,
			FreeMarginRaw: 9000, Currency: "USC", FetchedAt: ts},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.InsertVenueAccountSnapshots(ctx, nil); err != nil {
		t.Fatalf("empty insert should no-op: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM fin_venue_account_snapshot`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("count = %d, want 2 (err=%v)", n, err)
	}
	var venue, vt, ccy string
	var eq float64
	if err := s.pool.QueryRow(ctx,
		`SELECT venue, venue_type, equity_raw, currency FROM fin_venue_account_snapshot WHERE venue='Exness'`).
		Scan(&venue, &vt, &eq, &ccy); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if venue != "Exness" || vt != "mt5" || eq != 10050 || ccy != "USC" {
		t.Errorf("row = %s/%s/%f/%s, want Exness/mt5/10050/USC", venue, vt, eq, ccy)
	}
}
