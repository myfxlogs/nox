package pgstore

import (
	"context"
	"testing"
)

// TestAppConfig_LoadUpsert D-368 S2：Upsert 写/更新 + Load 全表读出（KV 语义）。
func TestAppConfig_LoadUpsert(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE app_config`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.UpsertAppConfig(ctx, "ARB_OPP_NOTIONAL_USD", "5000", "D-368 seed"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 更新同键（ON CONFLICT 走 UPDATE）
	if err := s.UpsertAppConfig(ctx, "ARB_OPP_NOTIONAL_USD", "6000", "updated"); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	rows, err := s.LoadAppConfig(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rows["ARB_OPP_NOTIONAL_USD"] != "6000" {
		t.Errorf("value = %q, want 6000（ON CONFLICT UPDATE）", rows["ARB_OPP_NOTIONAL_USD"])
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1（无重复键）", len(rows))
	}
}
