// d508_facts_test.go —— D-508 S3：PruneFacts kind 级 TTL + LatestFactsByKind（DSN 门控）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestD508_PruneFacts(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now()
	seed := []struct {
		kind string
		age  time.Duration
		gone bool
	}{
		{"swap_long", 40 * 24 * time.Hour, true},   // 35d 窗外
		{"swap_long", 10 * 24 * time.Hour, false},  // 35d 窗内
		{"trade_mode", 20 * 24 * time.Hour, true},  // 14d 窗外
		{"trade_mode", 5 * 24 * time.Hour, false},  // 14d 窗内
		{"custom_kind", 31 * 24 * time.Hour, true}, // 未列名默认 30d 窗外
		{"custom_kind", 2 * 24 * time.Hour, false},
		{"opt_parity_dev", 40 * 24 * time.Hour, false}, // 专属 7d 通道，本 prune 不动
	}
	for i, r := range seed {
		if _, err := s.pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1,'vtest','SYM',$2,'u',$3,'d508test')`,
			r.kind, float64(i), now.Add(-r.age)); err != nil {
			t.Fatalf("seed %s: %v", r.kind, err)
		}
	}
	n, err := s.PruneFacts(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("删行数 want=3 got=%d", n)
	}
	var cnt int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM facts WHERE src='d508test'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 4 {
		t.Fatalf("留存 want=4 got=%d", cnt)
	}
}

func TestD508_LatestFactsByKind(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now()
	for _, r := range []struct {
		venue, sym string
		v          float64
		age        time.Duration
	}{
		{"va", "A", 1, 2 * time.Hour},
		{"va", "A", 2, time.Hour}, // 最新
		{"vb", "B", 3, time.Hour},
		{"vc", "", 4, time.Hour}, // 账号级 NULL symbol 行
	} {
		var sym any = r.sym
		if r.sym == "" {
			sym = nil
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('swap_long',$1,$2,$3,'u',$4,'d508test')`,
			r.venue, sym, r.v, now.Add(-r.age)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.LatestFactsByKind(ctx, "swap_long")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("distinct venue,symbol want=3 got=%d", len(out))
	}
	for _, f := range out {
		if f.Venue == "va" && f.Value != 2 {
			t.Fatalf("va 应取最新值 2, got %v", f.Value)
		}
	}
}
