package pgstore

import (
	"context"
	"testing"
)

// TestWatchlist D-090 S1：建表迁移 / Get 空=空 / Set 全量替换幂等 / 顺序保持。
// DSN-gated：无 ARB_TEST_PG_DSN 跳过（go test ./... 无 PG 环境仍全过）。
func TestWatchlist(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 建表（migrate 已跑 0039，但 testStore 用独立库可能未跑；显式建表幂等）
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS watchlist_symbols (
			canonical text PRIMARY KEY,
			sort      integer NOT NULL DEFAULT 0,
			added_at  timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create watchlist_symbols: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE watchlist_symbols`); err != nil {
		t.Fatalf("truncate watchlist: %v", err)
	}

	// 1. 空表 Get = 空切片（非 nil 切片也接受，但这里期望无行）
	got, err := s.GetWatchlist(ctx)
	if err != nil {
		t.Fatalf("GetWatchlist empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("空表 GetWatchlist 返回 %d 行, want 0", len(got))
	}

	// 2. Set 后 Get 顺序一致
	want := []string{"AUDJPY", "USDJPY", "XAUUSD", "BTCUSDT"}
	if err := s.SetWatchlist(ctx, want); err != nil {
		t.Fatalf("SetWatchlist: %v", err)
	}
	got, err = s.GetWatchlist(ctx)
	if err != nil {
		t.Fatalf("GetWatchlist after set: %v", err)
	}
	if !sliceEq(got, want) {
		t.Errorf("Set 后 Get 顺序不一致: got %v, want %v", got, want)
	}

	// 3. 二次 Set 全量替换（不同清单）
	want2 := []string{"EURUSD", "GBPUSD"}
	if err := s.SetWatchlist(ctx, want2); err != nil {
		t.Fatalf("SetWatchlist 2nd: %v", err)
	}
	got, err = s.GetWatchlist(ctx)
	if err != nil {
		t.Fatalf("GetWatchlist after 2nd set: %v", err)
	}
	if !sliceEq(got, want2) {
		t.Errorf("二次 Set 未全量替换: got %v, want %v", got, want2)
	}

	// 4. 幂等：重复调同入参结果一致
	if err := s.SetWatchlist(ctx, want2); err != nil {
		t.Fatalf("SetWatchlist idempotent: %v", err)
	}
	got, err = s.GetWatchlist(ctx)
	if err != nil {
		t.Fatalf("GetWatchlist idempotent: %v", err)
	}
	if !sliceEq(got, want2) {
		t.Errorf("幂等失败: got %v, want %v", got, want2)
	}

	// 5. 空清单=清空（合法）
	if err := s.SetWatchlist(ctx, nil); err != nil {
		t.Fatalf("SetWatchlist clear: %v", err)
	}
	got, err = s.GetWatchlist(ctx)
	if err != nil {
		t.Fatalf("GetWatchlist after clear: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("清空失败: got %v, want 空", got)
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
