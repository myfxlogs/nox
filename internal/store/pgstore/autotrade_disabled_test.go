package pgstore

import (
	"context"
	"testing"
)

// TestListDisabledStrategies DSN-gated 真库测试：插入 disabled=true/false 行，验证只返回 disabled=true 集合。
func TestListDisabledStrategies(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, `TRUNCATE strategy_stats, fin_strategy`); err != nil {
		t.Fatalf("TRUNCATE strategy_stats: %v", err)
	}
	// 插入 3 行：2 disabled + 1 enabled
	for _, row := range []struct {
		name     string
		disabled bool
	}{
		{"CROSS_VENUE", true},
		{"PERP_FUNDING", true},
		{"SWAPARB", false},
	} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO strategy_stats (strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, disabled) VALUES ($1, 0, 0, 0, 0, 0, 0, $2)`,
			row.name, row.disabled); err != nil {
			t.Fatalf("insert strategy_stats %s: %v", row.name, err)
		}
	}
	got, err := s.ListDisabledStrategies(ctx)
	if err != nil {
		t.Fatalf("ListDisabledStrategies: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("disabled count want 2, got %d (%v)", len(got), got)
	}
	if !got["CROSS_VENUE"] || !got["PERP_FUNDING"] {
		t.Errorf("missing expected disabled strategies, got %v", got)
	}
	if got["SWAPARB"] {
		t.Errorf("SWAPARB should not be disabled, got %v", got)
	}
}

// TestListDisabledStrategies_EmptyTable 空表 → 空集合（不报错）。
func TestListDisabledStrategies_EmptyTable(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, `TRUNCATE strategy_stats, fin_strategy`); err != nil {
		t.Fatalf("TRUNCATE strategy_stats: %v", err)
	}
	got, err := s.ListDisabledStrategies(ctx)
	if err != nil {
		t.Fatalf("ListDisabledStrategies: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty table disabled count want 0, got %d", len(got))
	}
}
