package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// CN-FI 落库测试（D-207 loud-FAIL via testStore；D-234 P0-1 教训：绝不绕过 TestDSN）。
// 共享 arb_test 库（B-1 债）：哨兵日期 1970-01-02（真实数据永不含）+ defer 自清理。
const cnfiTestDay = "1970-01-02"

func TestUpsertCNFISeries_IdempotentAndChanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DELETE FROM cnfi_series WHERE as_of = $1`, cnfiTestDay)
	})
	base := []CNFIPoint{{AsOf: cnfiTestDay, Series: "cgb", Tenor: "10y", ValueBP: 168.04}}
	w1, c1, err := s.UpsertCNFISeries(ctx, base, "chinabond", "test-run-1", time.Now().UTC())
	if err != nil || w1 != 1 || c1 != 0 {
		t.Fatalf("first insert: written=%d changed=%d err=%v (want 1/0/nil)", w1, c1, err)
	}
	// 同值重跑 = 幂等（冲突+WHERE 过滤 → ErrNoRows 路径 → 0/0）
	w2, c2, err := s.UpsertCNFISeries(ctx, base, "chinabond", "test-run-2", time.Now().UTC())
	if err != nil || w2 != 0 || c2 != 0 {
		t.Fatalf("idempotent rerun: written=%d changed=%d err=%v (want 0/0/nil)", w2, c2, err)
	}
	// 值修订 = changed
	rev := []CNFIPoint{{AsOf: cnfiTestDay, Series: "cgb", Tenor: "10y", ValueBP: 168.05}}
	w3, c3, err := s.UpsertCNFISeries(ctx, rev, "chinabond", "test-run-3", time.Now().UTC())
	if err != nil || w3 != 0 || c3 != 1 {
		t.Fatalf("revision: written=%d changed=%d err=%v (want 0/1/nil)", w3, c3, err)
	}
	// run 记录闭环
	runID, err := s.StartCNFIRun(ctx, "daily")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	maxAsOf := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	if err := s.FinishCNFIRun(ctx, runID, "success", &maxAsOf, 3, 1, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	var status string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM cnfi_probe_runs WHERE id=$1`, runID).Scan(&status); err != nil || status != "success" {
		t.Fatalf("run status = %q err=%v (want success)", status, err)
	}
}

func TestUpsertCNFISeries_InvalidTenorRejected(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	bad := []CNFIPoint{{AsOf: cnfiTestDay, Series: "cgb", Tenor: "2.5y", ValueBP: 100}}
	_, _, err := s.UpsertCNFISeries(ctx, bad, "chinabond", "test-run-bad", time.Now().UTC())
	if err == nil {
		t.Fatal("invalid tenor must be rejected by CHECK constraint")
	}
	if !strings.Contains(err.Error(), "cnfi_series_tenor_check") {
		t.Fatalf("expected tenor CHECK violation, got: %v", err)
	}
}
