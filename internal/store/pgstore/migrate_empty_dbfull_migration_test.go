package pgstore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
)

// S3.3：安全测试库 migration 可重复验证。
// 从空库执行全量 migration，确认无错误 + 所有表创建成功 + migration 版本号正确。

// TestMigrate_EmptyDBFullMigration：从空库执行全量 migration 成功。
// 用 admin DSN 建空库 → pgstore.New + Migrate → 验证表数量 + schema_migrations 版本。
func TestMigrate_EmptyDBFullMigration(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	emptyDB := "nox_migrate_empty_test_" + testutil.SeatTag() // D-016 席位后缀：多席并发不互踩

	// 清理旧库
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("DROP DATABASE IF EXISTS %s;", emptyDB)).CombinedOutput(); err != nil {
		t.Fatalf("drop old empty db: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.CommandContext(context.Background(), "psql", admin, "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", emptyDB)).Run()
	})

	// 建空库（无 OWNER 子句——owner 默认=当前连接用户，本地 arb/CI postgres 两端通）
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("CREATE DATABASE %s;", emptyDB)).CombinedOutput(); err != nil {
		t.Fatalf("create empty db: %v\n%s", err, out)
	}

	// 推导空库 DSN（保留 pgx 参数——pgstore.New 用 pgxpool，认 pool 参数）
	emptyDSN := swapDBName(testutil.TestDSN(t, "NOX_TEST_PG_DSN"), emptyDB)

	// pgstore.New + Migrate（从空库执行全量 migration）
	s, err := New(ctx, emptyDSN)
	if err != nil {
		t.Fatalf("pgstore.New on empty db: %v", err)
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate on empty db: %v", err)
	}

	// 验证 schema_migrations 版本数 = migrationFiles 长度
	var versionCount int
	err = s.pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version<90000").Scan(&versionCount)
	if err != nil {
		t.Fatalf("query schema_migrations count: %v", err)
	}
	if versionCount != len(migrationFiles) {
		t.Errorf("schema_migrations 行数=%d, want %d（全量 migration 版本数）",
			versionCount, len(migrationFiles))
	}

	// 验证关键表存在（抽样检查 D-195 相关的新表）
	keyTables := []string{
		"execution_controls",
		"live_gates",
		"position_legs",
		"position_cashflows",
		"position_settlements",
	}
	for _, tbl := range keyTables {
		var exists bool
		err = s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`,
			tbl).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("关键表 %s 不存在（全量 migration 应创建）", tbl)
		}
	}

	// 验证 execution_controls 初始行（migration 0062 插入 global/READ_ONLY）
	var mode string
	err = s.pool.QueryRow(ctx,
		`SELECT mode FROM execution_controls WHERE scope_type='global' AND scope_key=''`).Scan(&mode)
	if err != nil {
		t.Fatalf("query execution_controls initial row: %v", err)
	}
	if mode != "READ_ONLY" {
		t.Errorf("execution_controls 初始 mode=%s, want READ_ONLY", mode)
	}
}

// TestMigrate_Idempotent：对已 migrate 的库再跑 Migrate 无错误（幂等）。
func TestMigrate_Idempotent(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()

	// nox_test 已 migrate（testStore/prepareTestSchema 已跑），再跑一次应无错误
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 幂等再跑失败: %v", err)
	}

	// 确认版本数不变
	var versionCount int
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version<90000").Scan(&versionCount)
	if versionCount != len(migrationFiles) {
		t.Errorf("schema_migrations 行数=%d, want %d（幂等再跑不应改变版本数）",
			versionCount, len(migrationFiles))
	}
}

// 确保 strings 包被引用
var _ = strings.TrimSpace

// TestMigrate_LiveModeCheck D-620 T5：1050 后 execution_controls CHECK 收 'LIVE' 拒 'FOO'。
func TestMigrate_LiveModeCheck(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	defer s.pool.Exec(ctx, `DELETE FROM execution_controls WHERE scope_key='d620-t5'`)
	if _, err := s.pool.Exec(ctx, `INSERT INTO execution_controls (scope_type,scope_key,mode,enabled) VALUES ('account','d620-t5','LIVE',true)`); err != nil {
		t.Fatalf("CHECK 应收 LIVE: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO execution_controls (scope_type,scope_key,mode,enabled) VALUES ('account','d620-t5','FOO',true)`); err == nil {
		t.Fatal("CHECK 应拒 FOO")
	}
	// 部分唯一索引：active+revoked 共存不撞键（撤权后续权路径前置条件）。
	if _, err := s.pool.Exec(ctx, `INSERT INTO live_gates (scope_type,scope_key,account_key,strategy,operator,confirmed_at,check_snapshot) VALUES ('global','d620-t5','','','op',now(),'{}'::jsonb)`); err != nil {
		t.Fatalf("live_gates 首插: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE live_gates SET revoked_at=now(),revoke_reason='t' WHERE scope_key='d620-t5'`); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO live_gates (scope_type,scope_key,account_key,strategy,operator,confirmed_at,check_snapshot) VALUES ('global','d620-t5','','','op2',now(),'{}'::jsonb)`); err != nil {
		t.Fatalf("撤权后再插应不撞键（部分唯一索引）: %v", err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM live_gates WHERE scope_key='d620-t5'`)
}
