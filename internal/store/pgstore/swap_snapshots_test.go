package pgstore

import (
	"context"
	"testing"
	"time"
)

// D-110 P2: position_swap_snapshots 集成测试（DSN-gated）。
// 参考 investment_records_test.go 的 testStore 模式。

func testSwapSnapshotStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_swap_snapshots CASCADE"); err != nil {
		t.Fatalf("TRUNCATE position_swap_snapshots: %v", err)
	}
	return s
}

func TestInsertAndLatestSwapSnapshot(t *testing.T) {
	s := testSwapSnapshotStore(t)
	ctx := context.Background()
	// 先建一个 position 供外键引用
	var posID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', now()) RETURNING id`,
		"D110TEST:SYM:vA:vB").Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	ts1 := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	if err := s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 5.0, ts1); err != nil {
		t.Fatalf("insert snap1: %v", err)
	}
	if err := s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 10.0, ts2); err != nil {
		t.Fatalf("insert snap2: %v", err)
	}
	got, err := s.LatestSwapSnapshot(ctx, posID, "venue:mt5")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got.SwapUSD != 10.0 {
		t.Fatalf("expected swap=10.0, got %.4f", got.SwapUSD)
	}
}

func TestLatestSwapSnapshotBefore(t *testing.T) {
	s := testSwapSnapshotStore(t)
	ctx := context.Background()
	var posID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', now()) RETURNING id`,
		"D110TEST2:SYM:vA:vB").Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	ts1 := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 5.0, ts1)
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 10.0, ts2)
	// before = ts2 → 返回 ts1
	got, err := s.LatestSwapSnapshotBefore(ctx, posID, "venue:mt5", ts2)
	if err != nil {
		t.Fatalf("latest before: %v", err)
	}
	if got.SwapUSD != 5.0 {
		t.Fatalf("expected swap=5.0, got %.4f", got.SwapUSD)
	}
}

func TestDailySwapAccrued(t *testing.T) {
	s := testSwapSnapshotStore(t)
	ctx := context.Background()
	var posID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', now()) RETURNING id`,
		"D110TEST3:SYM:vA:vB").Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	yesterday := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	today := time.Date(2026, 8, 26, 1, 0, 0, 0, time.UTC)
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 5.0, yesterday)
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 9.84, today)
	day := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	got, ok, err := s.DailySwapAccrued(ctx, posID, "venue:mt5", day)
	if err != nil {
		t.Fatalf("daily accrued: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	expected := 9.84 - 5.0
	if got != expected {
		t.Fatalf("expected %.4f, got %.4f", expected, got)
	}
}

func TestDailySwapAccrued_NoSnapshot(t *testing.T) {
	s := testSwapSnapshotStore(t)
	ctx := context.Background()
	var posID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', now()) RETURNING id`,
		"D110TEST4:SYM:vA:vB").Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	day := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	_, ok, err := s.DailySwapAccrued(ctx, posID, "venue:mt5", day)
	if err != nil {
		t.Fatalf("daily accrued: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for no snapshot")
	}
}

func TestDailySwapAccrued_TodayNotSettled(t *testing.T) {
	s := testSwapSnapshotStore(t)
	ctx := context.Background()
	var posID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', now()) RETURNING id`,
		"D110TEST5:SYM:vA:vB").Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// 今日末条 ts <= day_start（8-26 00:00）
	yesterday := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	earlyToday := time.Date(2026, 8, 25, 23, 0, 0, 0, time.UTC) // before day_start 8-26 00:00
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 5.0, yesterday)
	_ = s.InsertSwapSnapshot(ctx, posID, "venue:mt5", 8.0, earlyToday)
	day := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	_, ok, err := s.DailySwapAccrued(ctx, posID, "venue:mt5", day)
	if err != nil {
		t.Fatalf("daily accrued: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when today not settled")
	}
}
