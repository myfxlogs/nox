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

// TestLatestVenueAccountSnapshots_DualType D-358fix 锚：同 venue 双 venue_type → 两行
// （各自取最新；旧 DISTINCT ON (venue) 单键把 okx spot/perp 折叠成一行=漏池）。
func TestLatestVenueAccountSnapshots_DualType(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_venue_account_snapshot"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		{Venue: "okx", VenueType: "spot", EquityRaw: 100, Currency: "USDT", FetchedAt: now.Add(-2 * time.Hour)},
		{Venue: "okx", VenueType: "perp", EquityRaw: 200, Currency: "USDT", FetchedAt: now.Add(-2 * time.Hour)},
		// spot 新一轮 300（应胜出）；perp 旧轮 250（比现有 200 更旧，应被该 type 折叠掉）
		{Venue: "okx", VenueType: "spot", EquityRaw: 300, Currency: "USDT", FetchedAt: now},
		{Venue: "okx", VenueType: "perp", EquityRaw: 250, Currency: "USDT", FetchedAt: now.Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	lates, err := s.LatestVenueAccountSnapshots(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(lates) != 2 {
		t.Fatalf("rows = %d, want 2（同 venue 双 type 不折叠）: %+v", len(lates), lates)
	}
	// ORDER BY venue, venue_type → perp 前 spot 后；各取自身 venue_type 最新
	if lates[0].VenueType != "perp" || lates[0].EquityRaw == nil || *lates[0].EquityRaw != 200 {
		t.Errorf("rows[0] = %s equity %v, want perp/200（旧轮 250 被折叠）", lates[0].VenueType, lates[0].EquityRaw)
	}
	if lates[1].VenueType != "spot" || lates[1].EquityRaw == nil || *lates[1].EquityRaw != 300 {
		t.Errorf("rows[1] = %s equity %v, want spot/300（最新轮胜出）", lates[1].VenueType, lates[1].EquityRaw)
	}
}
