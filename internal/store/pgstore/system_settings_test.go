package pgstore

import (
	"context"
	"testing"
)

// systemSettingsTestStore 连接 ARB_TEST_PG_DSN 测试库，TRUNCATE system_settings 隔离数据。
func systemSettingsTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE system_settings"); err != nil {
		t.Fatalf("TRUNCATE system_settings: %v", err)
	}
	// 迁移 INSERT ON CONFLICT DO NOTHING 已建初始行（id=1, tronscan_base=NULL），
	// TRUNCATE 清掉后需补回初始行保证 Get 不报 ErrNoRows。
	if _, err := s.pool.Exec(ctx, `INSERT INTO system_settings (id, tronscan_base) VALUES (1, NULL) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("reseed system_settings: %v", err)
	}
	return s
}

// TestSystemSettings_GetSet 验证 Set 非空→Get 返回该值；Set 空→Get 返回原值（不改动）。
func TestSystemSettings_GetSet(t *testing.T) {
	s := systemSettingsTestStore(t)
	ctx := context.Background()

	// 初始：tronscan_base=NULL → Get 返回空串
	got, err := s.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings initial: %v", err)
	}
	if got.TronscanBase != "" {
		t.Fatalf("initial tronscan_base want empty, got %q", got.TronscanBase)
	}

	// Set 非空 → Get 返回该值
	if err := s.SetSystemSettings(ctx, "https://apilist.tronscan.org"); err != nil {
		t.Fatalf("SetSystemSettings non-empty: %v", err)
	}
	got, err = s.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings after set: %v", err)
	}
	if got.TronscanBase != "https://apilist.tronscan.org" {
		t.Fatalf("after set tronscan_base want https://apilist.tronscan.org, got %q", got.TronscanBase)
	}

	// Set 空 → Get 返回原值（不改动）
	if err := s.SetSystemSettings(ctx, ""); err != nil {
		t.Fatalf("SetSystemSettings empty: %v", err)
	}
	got, err = s.GetSystemSettings(ctx)
	if err != nil {
		t.Fatalf("GetSystemSettings after empty set: %v", err)
	}
	if got.TronscanBase != "https://apilist.tronscan.org" {
		t.Fatalf("after empty set tronscan_base want unchanged https://apilist.tronscan.org, got %q", got.TronscanBase)
	}
}

// TestSystemSettings_GetNotFound 行不存在 → 返回 error（ErrNoRows）。
func TestSystemSettings_GetNotFound(t *testing.T) {
	s := systemSettingsTestStore(t)
	ctx := context.Background()
	// 删掉初始行模拟不存在
	if _, err := s.pool.Exec(ctx, "DELETE FROM system_settings WHERE id = 1"); err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if _, err := s.GetSystemSettings(ctx); err == nil {
		t.Fatal("GetSystemSettings on empty table: want error, got nil")
	}
}
