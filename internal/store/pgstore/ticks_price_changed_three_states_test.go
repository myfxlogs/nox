// ticks_d495_test.go —— D-495 S7①：TicksPriceChanged 三态（无 tick / 单值冻结 / 多值活动）。
// 需 NOX_TEST_PG_DSN（pgstore 包测试库口径）。
package pgstore

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
)

func d495Store(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatalf("ensure partition: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM ticks WHERE venue = 'd495v'`); err != nil {
		t.Fatalf("clean ticks: %v", err)
	}
	return s
}

func TestD495_TicksPriceChanged_ThreeStates(t *testing.T) {
	s := d495Store(t)
	ctx := context.Background()
	window := 30 * time.Minute

	// 态1：无 tick → 不可判可交易
	changed, n, err := s.TicksPriceChanged(ctx, "d495v", "NOSUCH", window)
	if err != nil {
		t.Fatalf("no-tick: %v", err)
	}
	if changed || n != 0 {
		t.Fatalf("no-tick want changed=false n=0, got %v n=%d", changed, n)
	}

	// 态2：窗口内多行同 bid（冻结报价/心跳重复值）→ 仍不可判
	if err := s.InsertTicks(ctx, []TickRow{
		{Ts: time.Now().Add(-5 * time.Minute), Venue: "d495v", Symbol: "FROZEN", Bid: 100.0, Ask: 100.1, VenueType: "mt5"},
		{Ts: time.Now().Add(-2 * time.Minute), Venue: "d495v", Symbol: "FROZEN", Bid: 100.0, Ask: 100.1, VenueType: "mt5"},
		{Ts: time.Now(), Venue: "d495v", Symbol: "FROZEN", Bid: 100.0, Ask: 100.1, VenueType: "mt5"},
	}); err != nil {
		t.Fatalf("insert frozen: %v", err)
	}
	changed, n, err = s.TicksPriceChanged(ctx, "d495v", "FROZEN", window)
	if err != nil {
		t.Fatalf("frozen: %v", err)
	}
	if changed || n != 1 {
		t.Fatalf("frozen want changed=false n=1, got %v n=%d", changed, n)
	}

	// 态3：窗口内 bid 变过 → 可交易；窗口外老数据不算数
	if err := s.InsertTicks(ctx, []TickRow{
		{Ts: time.Now().Add(-10 * time.Minute), Venue: "d495v", Symbol: "LIVE", Bid: 200.0, Ask: 200.1, VenueType: "mt5"},
		{Ts: time.Now().Add(-1 * time.Minute), Venue: "d495v", Symbol: "LIVE", Bid: 200.5, Ask: 200.6, VenueType: "mt5"},
		// 窗口外第三值：不得影响判定
		{Ts: time.Now().Add(-2 * time.Hour), Venue: "d495v", Symbol: "LIVE", Bid: 199.0, Ask: 199.1, VenueType: "mt5"},
	}); err != nil {
		t.Fatalf("insert live: %v", err)
	}
	changed, n, err = s.TicksPriceChanged(ctx, "d495v", "LIVE", window)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if !changed || n != 2 {
		t.Fatalf("live want changed=true n=2, got %v n=%d", changed, n)
	}
}
