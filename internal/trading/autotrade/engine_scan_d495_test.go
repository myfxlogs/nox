// engine_scan_d495_test.go —— D-495 S3/S7②：scanAndClose 前置全腿可平性闸 +
// suspend 转场 warn 去重。需 ARB_TEST_PG_DSN。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/platform/testutil"
	"arb/internal/store/pgstore"
)

func d495Engine(t *testing.T) (*Engine, *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	s, err := pgstore.New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	// ticks 无 TRUNCATE（共享库他包数据）——按本测试 venue 定点清，保证 30min 窗内重跑自净。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM ticks WHERE venue IN ('d495a','d495b')`); err != nil {
		t.Fatalf("clean ticks: %v", err)
	}
	return &Engine{store: s, legSuspend: map[int64]bool{}}, s
}

func TestD495_ScanGate_SuspendTransition(t *testing.T) {
	e, s := d495Engine(t)
	ctx := context.Background()

	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('SWAPARB:XAUUSD:d495', 'SWAPARB', 'OPEN', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	seedLeg := func(legNo int16, venue, sym string) {
		if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID: posID, LegNo: legNo, Phase: "OPEN", Venue: venue,
			Symbol: sym, Side: "BUY", Qty: 0.01, Status: "FILLED",
		}); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	seedLeg(0, "d495a:mt5", "XAUUSDm") // 暗（无 ticks）
	seedLeg(1, "d495b:mt5", "XAUUSDm")

	// 暗腿 → 不可行动；suspend 转场 warn 单条
	if ok, _ := e.allLegsActionable(ctx, posID); ok {
		t.Fatal("双腿无 ticks 应不可行动")
	}
	e.markLegSuspend(ctx, posID, "test-dark")
	e.markLegSuspend(ctx, posID, "test-dark") // 同型重复 → 不再报
	var warnN int
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE position_id=$1 AND level='warn'`, posID).Scan(&warnN); err != nil {
		t.Fatalf("warn count: %v", err)
	}
	if warnN != 1 {
		t.Fatalf("suspend 转场应单条 warn, got %d", warnN)
	}

	// 播亮双腿 → 恢复可行动 + 出 suspend（下轮同型可再报）
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: time.Now().Add(-time.Minute), Venue: "d495a", Symbol: "XAUUSDm", Bid: 1, Ask: 1.1, VenueType: "mt5"},
		{Ts: time.Now(), Venue: "d495a", Symbol: "XAUUSDm", Bid: 1.5, Ask: 1.6, VenueType: "mt5"},
		{Ts: time.Now().Add(-time.Minute), Venue: "d495b", Symbol: "XAUUSDm", Bid: 2, Ask: 2.1, VenueType: "mt5"},
		{Ts: time.Now(), Venue: "d495b", Symbol: "XAUUSDm", Bid: 2.5, Ask: 2.6, VenueType: "mt5"},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
	if ok, _ := e.allLegsActionable(ctx, posID); !ok {
		t.Fatal("双腿亮后应可行动")
	}
	e.clearLegSuspend(posID)
	e.markLegSuspend(ctx, posID, "test-dark-again")
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE position_id=$1 AND level='warn'`, posID).Scan(&warnN); err != nil {
		t.Fatalf("warn recount: %v", err)
	}
	if warnN != 2 {
		t.Fatalf("恢复后再入 suspend 应再报一条, got %d", warnN)
	}
}
