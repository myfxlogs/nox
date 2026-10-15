// venue_account_snapshot_throttle_test.go —— D-545 锚：rest 系 ε+心跳节流
// （事件径零动）、保留窗每键最新行永保。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func snapRow(src, venue, ccy string, eq, bal, fm float64, at time.Time) VenueAccountSnapshot {
	return VenueAccountSnapshot{Venue: venue, VenueType: "spot", BalanceRaw: bal,
		EquityRaw: eq, FreeMarginRaw: fm, Currency: ccy, FetchedAt: at, Src: src}
}

func countSnaps(t *testing.T, s *Store, venue, ccy string) int {
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM fin_venue_account_snapshot WHERE venue=$1 AND venue_type='spot' AND currency=$2`,
		venue, ccy).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSnapThrottle_RestEpsHeartbeat(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	key := "thr" + now.Format("150405.000")
	// 首行（无历史必写）
	if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		snapRow("rest_poll", key, "USDT", 100, 100, 50, now)}); err != nil {
		t.Fatal(err)
	}
	// ε 内浮值+10min 窗内 → 跳写
	if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		snapRow("rest_poll", key, "USDT", 100.005, 100.005, 50, now.Add(2*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if n := countSnaps(t, s, key, "USDT"); n != 1 {
		t.Fatalf("ε 内跳写破: n=%d", n)
	}
	// 超 ε（100→101）→ 写
	if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		snapRow("rest_poll", key, "USDT", 101, 101, 50, now.Add(4*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	// 窗内值未变但 ≥10min 心跳 → 写
	if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		snapRow("rest_poll", key, "USDT", 101, 101, 50, now.Add(15*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	// 事件径零节流——ε 内同值也写
	if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
		snapRow("okx_event", key, "USDT", 101, 101, 50, now.Add(16*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if n := countSnaps(t, s, key, "USDT"); n != 4 {
		t.Fatalf("n=%d want 4（首+变+心跳+事件）", n)
	}
}

func TestSnapPrune_KeepsLatestPerKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	// 活键：两行老+一行新 → 老两行删
	key := "prz" + now.Format("150405.000")
	for _, at := range []time.Time{old, old.Add(time.Hour), now} {
		if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
			snapRow("scheduler", key, "USDT", 100, 100, 0, at)}); err != nil {
			t.Fatal(err)
		}
	}
	// 化石键：最新行即 8 天前（断采证据）→ 一行不删
	fossil := "fossil" + now.Format("150405.000")
	for _, at := range []time.Time{old, old.Add(2 * time.Hour)} {
		if err := s.InsertVenueAccountSnapshots(ctx, []VenueAccountSnapshot{
			snapRow("rest_poll", fossil, "USDT", 5, 5, 0, at)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneVenueAccountSnapshots(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 { // 活键 2 老行 + 化石键 1 老行（第二新行亦老但后有更新行）
		t.Fatalf("prune n=%d want ≥3", n)
	}
	if c := countSnaps(t, s, key, "USDT"); c != 1 {
		t.Fatalf("活键剩 %d want 1（最新行）", c)
	}
	if c := countSnaps(t, s, fossil, "USDT"); c != 1 {
		t.Fatalf("化石键剩 %d want 1（最新行永保）", c)
	}
}
