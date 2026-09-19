// D-270 R2 S6 测试：cnfi_repo_config Store 方法（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
)

// repoConfigTestStore 连接 ARB_TEST_PG_DSN 测试库，迁移后直接测试（单行表 id=1 由 migration 插入）。
func repoConfigTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	return s
}

// TestRepoConfig_GetInitial 读 migration 插入的初始行（idle=0, min_return=0, hour=10, enabled=true）。
func TestRepoConfig_GetInitial(t *testing.T) {
	s := repoConfigTestStore(t)
	ctx := context.Background()
	cfg, err := s.GetRepoConfig(ctx)
	if err != nil {
		t.Fatalf("GetRepoConfig: %v", err)
	}
	if cfg.IdleCny != 0 {
		t.Errorf("IdleCny = %v, want 0", cfg.IdleCny)
	}
	if cfg.MinReturnCny != 0 {
		t.Errorf("MinReturnCny = %v, want 0", cfg.MinReturnCny)
	}
	if cfg.ReminderHour != 10 {
		t.Errorf("ReminderHour = %v, want 10", cfg.ReminderHour)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled = false, want true")
	}
}

// TestRepoConfig_SetAndGet Set 新值后再读验证。
func TestRepoConfig_SetAndGet(t *testing.T) {
	s := repoConfigTestStore(t)
	ctx := context.Background()
	if err := s.SetRepoConfig(ctx, 50000, 5, 14, true); err != nil {
		t.Fatalf("SetRepoConfig: %v", err)
	}
	cfg, err := s.GetRepoConfig(ctx)
	if err != nil {
		t.Fatalf("GetRepoConfig: %v", err)
	}
	if cfg.IdleCny != 50000 {
		t.Errorf("IdleCny = %v, want 50000", cfg.IdleCny)
	}
	if cfg.MinReturnCny != 5 {
		t.Errorf("MinReturnCny = %v, want 5", cfg.MinReturnCny)
	}
	if cfg.ReminderHour != 14 {
		t.Errorf("ReminderHour = %v, want 14", cfg.ReminderHour)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled = false, want true")
	}
}

// TestRepoConfig_SetDisabled Set enabled=false 验证开关独立。
func TestRepoConfig_SetDisabled(t *testing.T) {
	s := repoConfigTestStore(t)
	ctx := context.Background()
	if err := s.SetRepoConfig(ctx, 10000, 0, 10, false); err != nil {
		t.Fatalf("SetRepoConfig: %v", err)
	}
	cfg, err := s.GetRepoConfig(ctx)
	if err != nil {
		t.Fatalf("GetRepoConfig: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("Enabled = true, want false")
	}
	if cfg.IdleCny != 10000 {
		t.Errorf("IdleCny = %v, want 10000", cfg.IdleCny)
	}
}
