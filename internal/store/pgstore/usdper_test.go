package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestUSDPer 七直盘推导全覆盖（D-087 返修包②）。
// 插假 ticks 行验：GBP→GBPUSD bid、AUD→AUDUSD bid、CHF→1/USDCHF bid、JPY→1/USDJPY bid。
// DSN-gated：无 NOX_TEST_PG_DSN 跳过（go test ./... 无 PG 环境仍全过）。
func TestUSDPer(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 清 ticks + 插 4 直盘假行（at=2026-08-15 12:00 UTC，落在 2026-08 分区内）
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, "TRUNCATE ticks CASCADE"); err != nil {
		t.Fatalf("TRUNCATE ticks: %v", err)
	}
	type tickRow struct {
		symbol string
		bid    float64
	}
	rows := []tickRow{
		{"GBPUSD", 1.27},  // 1 GBP = 1.27 USD
		{"AUDUSD", 0.65},  // 1 AUD = 0.65 USD
		{"USDCHF", 0.88},  // 1 USD = 0.88 CHF → 1 CHF = 1/0.88 USD
		{"USDJPY", 150.0}, // 1 USD = 150 JPY → 1 JPY = 1/150 USD
	}
	for _, r := range rows {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO ticks (symbol, bid, ask, ts, venue, venue_type)
			VALUES ($1, $2, $2, $3, 'test', 'spot')
		`, r.symbol, r.bid, at); err != nil {
			t.Fatalf("insert tick %s: %v", r.symbol, err)
		}
	}

	cases := []struct {
		quoteCcy string
		want     float64
	}{
		{"USD", 1.0},
		{"GBP", 1.27},
		{"AUD", 0.65},
		{"CHF", 1.0 / 0.88},
		{"JPY", 1.0 / 150.0},
		{"", 0},    // 未知→false
		{"XYZ", 0}, // 未知→false
	}
	for _, c := range cases {
		got, ok := s.USDPer(ctx, c.quoteCcy, at, "")
		if c.quoteCcy == "" || c.quoteCcy == "XYZ" {
			if ok {
				t.Errorf("USDPer(%q) ok=true, want false", c.quoteCcy)
			}
			continue
		}
		if !ok {
			t.Errorf("USDPer(%q) ok=false, want true", c.quoteCcy)
			continue
		}
		// 0.1% 容差
		diff := got - c.want
		if diff < 0 {
			diff = -diff
		}
		if diff > c.want*0.001 {
			t.Errorf("USDPer(%q) = %.6f, want %.6f", c.quoteCcy, got, c.want)
		}
	}
}
