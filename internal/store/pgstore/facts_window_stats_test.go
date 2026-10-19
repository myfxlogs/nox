// d412_facts_window_test.go —— D-412 S3/S6：FactsWindowStats 窗口均值读口（DSN 门控）。
// 7d 窗内均值含新旧行、窗外行不参与、零样本=ErrNoFactsInWindow。
package pgstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestD412_FactsWindowStats(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now()
	seed := []struct {
		value float64
		age   time.Duration
	}{
		{0, 6 * 24 * time.Hour},       // 窗内（7d 边界内）
		{-1.4133, 3 * 24 * time.Hour}, // 窗内
		{-1.4133, time.Hour},          // 窗内（最新）
		{-500, 10 * 24 * time.Hour},   // 窗外（10d）不参与均值
	}
	for _, r := range seed {
		if _, err := s.pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('swap_long','39050818','XAUUSD',$1,'bps_daily',$2,'test')`,
			r.value, now.Add(-r.age)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	mean, err := s.FactsWindowStats(ctx, "swap_long", "39050818", "XAUUSD", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := (0 - 1.4133 - 1.4133) / 3
	if mean < want-0.001 || mean > want+0.001 {
		t.Errorf("窗口均值 want ≈%.4f（窗外 −500 不参与），got %v", want, mean)
	}
	if _, err := s.FactsWindowStats(ctx, "swap_long", "39050818", "NOSUCH", 7*24*time.Hour); !errors.Is(err, ErrNoFactsInWindow) {
		t.Errorf("零样本应 ErrNoFactsInWindow, got %v", err)
	}
}

// TestD412b_EmptySymbol42P18：42P18 坑回归——symbol="" 时谓词不占参位，
// 参绑定随谓词分派（修前 $3 无处绑定 → could not determine data type of parameter $3）。
func TestD412b_EmptySymbol42P18(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('swap_long','39050818',NULL,-1.4,'bps_daily',now(),'test')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := s.FactsSince(ctx, "swap_long", "39050818", "", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("FactsSince empty symbol: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 NULL-symbol row, got %d", len(rows))
	}
	if _, err := s.FactsWindowStats(ctx, "swap_long", "39050818", "", time.Hour); err != nil {
		t.Fatalf("FactsWindowStats empty symbol: %v", err)
	}
	if _, err := s.FundingPredWindow(ctx, "39050818", "", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("FundingPredWindow empty symbol: %v", err)
	}
}
