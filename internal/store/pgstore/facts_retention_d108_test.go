// facts_retention_d108_test.go —— D-108②锚：PruneFacts 终值快照永存 + 新档位——
// 全组超窗时组内最新行存活（停发符号末读数不灭）；新扩档（funding_next_settle 2d/
// trade_session 7d）生效；分组隔离（终值卫兵按 kind+venue+symbol 逐组判定）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func d108Seed(t *testing.T, s *Store, kind, venue, symbol string, age time.Duration, v float64) {
	t.Helper()
	ctx := context.Background()
	var sym any
	if symbol != "" {
		sym = symbol
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1,$2,$3,$4,'u',$5,'d108test')`,
		kind, venue, sym, v, time.Now().Add(-age)); err != nil {
		t.Fatalf("seed %s %s %s: %v", kind, venue, symbol, err)
	}
}

func d108Count(t *testing.T, s *Store, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM facts WHERE src='d108test' AND `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestD108_PruneTerminalSnapshot 全组超窗：最新行存活（终值快照永存）、更老行清除；
// funding_next_settle 2d 新档生效（3d 行有 1d 兄弟→删，旧缺省 30d 下会留存）。
func TestD108_PruneTerminalSnapshot(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	// 默认窗 kind：40d(终值,存) + 50d(有新兄弟,删)。
	d108Seed(t, s, "d108term", "va", "S1", 40*day, 1)
	d108Seed(t, s, "d108term", "va", "S1", 50*day, 2)
	// funding_next_settle 2d 档：1d(存) + 3d(删)。
	d108Seed(t, s, "funding_next_settle", "vb", "S2", 1*day, 3)
	d108Seed(t, s, "funding_next_settle", "vb", "S2", 3*day, 4)
	// trade_session 7d 档：2d(存) + 8d(删)。
	d108Seed(t, s, "trade_session", "vc", "S3", 2*day, 5)
	d108Seed(t, s, "trade_session", "vc", "S3", 8*day, 6)

	n, err := s.PruneFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应删 3 行, got %d", n)
	}
	if got := d108Count(t, s, `kind='d108term'`); got != 1 {
		t.Fatalf("终值快照应存 1 行, got %d", got)
	}
	var v float64
	if err := s.pool.QueryRow(ctx, `SELECT value FROM facts WHERE src='d108test' AND kind='d108term'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("终值应为组内最新 1, got %v", v)
	}
	if got := d108Count(t, s, `kind='funding_next_settle'`); got != 1 {
		t.Fatalf("next_settle 2d 档应存 1 行, got %d", got)
	}
	if got := d108Count(t, s, `kind='trade_session'`); got != 1 {
		t.Fatalf("trade_session 7d 档应存 1 行, got %d", got)
	}
}

// TestD108_PruneGroupIsolation 终值卫兵按组判定：同 kind 下 A 组全超窗（单行=终值，存）、
// B 组有新兄弟（老行删）互不牵连。
func TestD108_PruneGroupIsolation(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	d108Seed(t, s, "d108iso", "venueA", "S", 40*day, 7) // A 组仅此一行=终值，存
	d108Seed(t, s, "d108iso", "venueB", "S", 40*day, 8) // B 组老行，删
	d108Seed(t, s, "d108iso", "venueB", "S", 10*day, 9) // B 组新行，存
	if _, err := s.PruneFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if got := d108Count(t, s, `venue='venueA'`); got != 1 {
		t.Fatalf("A 组终值应存, got %d", got)
	}
	if got := d108Count(t, s, `venue='venueB'`); got != 1 {
		t.Fatalf("B 组应存新行 1 行, got %d", got)
	}
}

// TestD108_LatestFactsByKindSince ts 下界变体锚：界内最新行取到、界外行不进结果、
// since 零值回退无界（终值快照态组仍返回其末行——语义=「界内 latest」而非「全史 latest」，
// 调用方按 factTTL 语义使用）。
func TestD108_LatestFactsByKindSince(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatal(err)
	}
	h := time.Hour
	d108Seed(t, s, "funding", "binance", "XAUUSDT", 1*h, 11) // 界内
	d108Seed(t, s, "funding", "binance", "XAUUSDT", 3*h, 99) // 同组界外老行
	d108Seed(t, s, "funding", "okx", "XAUUSDT", 3*h, 7)      // 整组界外（无界内行）

	got, err := s.LatestFactsByKindSince(ctx, "funding", time.Now().Add(-2*h))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != 11 {
		t.Fatalf("界内应仅 binance/XAUUSDT 最新行 value=11, got %+v", got)
	}
	// 零值=无界回退：同原 LatestFactsByKind（okx 组界外终值照回）。
	all, err := s.LatestFactsByKindSince(ctx, "funding", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("无界应回 2 组终值, got %d", len(all))
	}
}
