package pgstore

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"nox/internal/platform/testutil"
)

// TestMigrate_FreshDB_AllVersions D-508 S6：分割器拆除（pgx simple protocol 整批）
// 后全量迁移链在空库实证落地——逐版本建库 → Migrate → schema_migrations 记满。
func TestMigrate_FreshDB_AllVersions(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	var db = "nox_migrate_fresh_d508_test_" + testutil.SeatTag() // D-016 席位后缀：多席并发不互踩固定名辅助库
	defer func() {
		exec.CommandContext(context.Background(), "psql", admin, "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", db)).Run()
	}()
	for _, c := range []string{
		fmt.Sprintf("DROP DATABASE IF EXISTS %s;", db),
		fmt.Sprintf("CREATE DATABASE %s;", db),
	} {
		if out, err := exec.CommandContext(ctx, "psql", admin, "-c", c).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", c, err, out)
		}
	}
	dsn := stripPgxParams(swapDBName(testutil.TestDSN(t, "NOX_TEST_PG_DSN"), db))
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("空库全量迁移失败（分割器拆除回归）: %v", err)
	}
	var n, max int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*), max(version) FROM schema_migrations`).Scan(&n, &max); err != nil {
		t.Fatalf("count: %v", err)
	}
	if max < 147 {
		t.Fatalf("迁移链未到 147: applied=%d max=%d", n, max)
	}
	// 幂等重跑：second Migrate 应零改动无错。
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("幂等重跑失败: %v", err)
	}
}
