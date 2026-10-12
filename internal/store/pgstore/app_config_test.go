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
	if err := s.UpsertAppConfig(ctx, "NOX_OPP_NOTIONAL_USD", "5000", "D-368 seed"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 更新同键（ON CONFLICT 走 UPDATE）
	if err := s.UpsertAppConfig(ctx, "NOX_OPP_NOTIONAL_USD", "6000", "updated"); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	rows, err := s.LoadAppConfig(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rows["NOX_OPP_NOTIONAL_USD"] != "6000" {
		t.Errorf("value = %q, want 6000（ON CONFLICT UPDATE）", rows["NOX_OPP_NOTIONAL_USD"])
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1（无重复键）", len(rows))
	}
}

// TestAppConfig_ListRows_NullNote D-523 R1 A8：播种行 note IS NULL（InsertAppConfigIfAbsent
// 不写 note 列=生产种子形态）→ ListAppConfigRows 必须容忍 NULL（COALESCE→空串），
// 裸 Scan *string 遇 NULL 崩的形态=本测试红。
func TestAppConfig_ListRows_NullNote(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE app_config`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.InsertAppConfigIfAbsent(ctx, "NOX_NULLNOTE_PROBE", "x"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var null bool
	if err := s.Pool().QueryRow(ctx, `SELECT note IS NULL FROM app_config WHERE key='NOX_NULLNOTE_PROBE'`).Scan(&null); err != nil || !null {
		t.Fatalf("precondition: note should be NULL, null=%v err=%v", null, err)
	}
	rows, err := s.ListAppConfigRows(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != "NOX_NULLNOTE_PROBE" || rows[0].Note != "" {
		t.Fatalf("row = %+v, want single row with empty Note", rows)
	}
}
