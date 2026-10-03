// d035_funding_avg_test.go —— D-035：FundingWindowAvg 单条 SQL 聚合读口锚。
// AVG/MAX/COUNT 窗内样本、kind='funding' 硬谓词（swap 行不计）、窗外行不参与、n=0 零样本语义。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestD035_FundingWindowAvg(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now()
	seed := []struct {
		kind string
		val  float64
		age  time.Duration
	}{
		{"funding", 30, time.Hour},           // 窗内（最新→maxTs）
		{"funding", 30, 3 * 24 * time.Hour},  // 窗内
		{"funding", 20, 6 * 24 * time.Hour},  // 窗内
		{"funding", 999, 8 * 24 * time.Hour}, // 窗外（ts>since 谓词排除）
		{"swap_long", -1.4, time.Hour},       // 异 kind 窗内（不计入）
	}
	for _, r := range seed {
		if _, err := s.pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1,'okx','XAUUSDT',$2,'pct_annual',$3,'d035test')`,
			r.kind, r.val, now.Add(-r.age)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	avg, maxTs, n, err := s.FundingWindowAvg(ctx, "okx", "XAUUSDT", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("n want 3（窗外/异 kind 不计），got %d", n)
	}
	if want := (30.0 + 30 + 20) / 3; avg < want-0.001 || avg > want+0.001 {
		t.Fatalf("avg want ≈%.4f，got %v", want, avg)
	}
	if d := now.Add(-time.Hour).Sub(maxTs); d < 0 || d > time.Minute {
		t.Fatalf("maxTs 应为最新 funding 行（≈now-1h），got %v（偏差 %v）", maxTs, d)
	}
	// n=0：窗内零样本 → avg/ts 零值，n=0（调用方按 n 判 fail-closed）
	avg0, ts0, n0, err := s.FundingWindowAvg(ctx, "okx", "NOSUCH", now.Add(-7*24*time.Hour))
	if err != nil || n0 != 0 || avg0 != 0 || !ts0.IsZero() {
		t.Fatalf("零样本应 n=0/avg=0/ts 零值，got n=%d avg=%v ts=%v err=%v", n0, avg0, ts0, err)
	}
}
