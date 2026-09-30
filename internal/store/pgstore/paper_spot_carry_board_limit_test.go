// paper_spot_carry_board_limit_test.go 热修 hotfix-paper：ListPaperSpotCarryBoard
// exited 行 paperExitRecentLimit=500 硬上限（OOM pprof 实证：5 万垃圾行×JSON+deflate）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestListPaperSpotCarryBoard_ExitedCapped500(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE paper_spot_carry CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 3 active + 600 exited（窗口内）
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO paper_spot_carry (venue_a, venue_b, venue_type_a, venue_type_b, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, status, exit_ts, structure)
		SELECT 'va', 'vb', 'spot', 'perp', 'ACT' || g, 'long', 1, 100, 1, 'active', NULL, 'spot_carry' FROM generate_series(1,3) g`); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO paper_spot_carry (venue_a, venue_b, venue_type_a, venue_type_b, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, status, exit_ts, structure)
		SELECT 'va', 'vb', 'spot', 'perp', 'EX' || g, 'long', 1, 100, 1, 'exited', now(), 'spot_carry' FROM generate_series(1,600) g`); err != nil {
		t.Fatalf("seed exited: %v", err)
	}
	rows, err := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	var active, exited int
	for _, r := range rows {
		switch r.Status {
		case "active", "demo":
			active++
		case "exited":
			exited++
		}
	}
	if exited != paperExitRecentLimit {
		t.Fatalf("exited=%d, want %d（硬上限）", exited, paperExitRecentLimit)
	}
	if active != 3 {
		t.Fatalf("active=%d, want 3（active 全量不受限）", active)
	}
}
