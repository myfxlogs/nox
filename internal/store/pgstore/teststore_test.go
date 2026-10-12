package pgstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// testStore 连接 ARB_TEST_PG_DSN 指定的专用测试库（可销毁数据）。
// 未设置时跳过：go test ./... 无 PG 环境仍全过。
// 安全闸（ref/arbcn 同款教训）：测试会对目标表 TRUNCATE，绝不允许指向非测试库——TestDSN fail-closed 门禁。
// 原 investment_records 清表随表清退移除。
func testStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(context.Background(), "TRUNCATE position_daily_income CASCADE"); err != nil {
		t.Fatalf("TRUNCATE position_daily_income: %v", err)
	}
	return s
}

// rebuildTestSchemaOnce 每个测试进程只重建一次测试库 schema（prepareTestSchema）。
var rebuildTestSchemaOnce sync.Once

// prepareTestSchema 将测试库重建为与生产完全同态的最新 schema（v1..N 全量真执行）。
// 背景（D-054 修）：旧 ensureBaseApplied 只把 v1..N-1 预标记为「已应用」、不执行其 SQL，
// 导致「最后一个迁移」每次前移时，前序版本的建表/改列（如 0009 opportunities ADD canonical）从不真跑
// ——测试库 schema 随迁移数漂移，每新增一个迁移就把边界前移一位 = 回归。且旧注释称 0001_init.sql 会建
// schema_migrations 实为过时（0001 并不建，migrate.go 的 IF NOT EXISTS 已兜底）。
// 改为每次测试进程 DROP schema + 全量 Migrate：schema 恒等于生产、确定性重建；安全闸 = testStore 的
// dbname 必须含 "test"（测试库可销毁数据，仅 pgstore 测试进程访问，无跨包并行冲突）。
func prepareTestSchema(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	var rebuildErr error
	rebuildTestSchemaOnce.Do(func() {
		if _, err := s.pool.Exec(ctx, `DROP SCHEMA public CASCADE`); err != nil {
			rebuildErr = fmt.Errorf("drop schema: %w", err)
			return
		}
		if _, err := s.pool.Exec(ctx, `CREATE SCHEMA public`); err != nil {
			rebuildErr = fmt.Errorf("create schema: %w", err)
			return
		}
		if err := s.Migrate(ctx); err != nil {
			rebuildErr = fmt.Errorf("migrate: %w", err)
			return
		}
	})
	if rebuildErr != nil {
		t.Fatalf("测试库重建失败: %v", rebuildErr)
	}
}
