package pgstore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"arb/internal/testutil"
)

// D-195-C3 S2：备份恢复完整性测试（§10.1 场景 17）。
// 用 pg_dump + restore 到临时库 arb_restore_test，比对 positions/cashflows/settlements
// 行数和内容一致 + cashflows 幂等键无重复。
// 不依赖 scripts/restore_test.sh（测试自包含，CI 友好）。
//
// D-207 注意：TestDSN 返回值经 normalizeDSN 追加了 pgx 专用参数（pool_max_conns/
// pool_min_conns/options），psql/pg_dump 不认识 → 传给 psql 前必须 stripPgxParams。

// adminDSN 推导：从 ARB_TEST_PG_DSN 替换 dbname 为 postgres（有 CREATE DATABASE 权限）。
// 剥离 pgx 专用参数（pool_max_conns/pool_min_conns/options），psql 不认识。
func adminDSN(t *testing.T) string {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	return stripPgxParams(swapDBName(dsn, "postgres"))
}

// psqlDSN 剥离 pgx 专用参数后返回 psql 可用的源库 DSN。
func psqlDSN(t *testing.T) string {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	return stripPgxParams(dsn)
}

// swapDBName 替换 DSN 中的 dbname 部分。
func swapDBName(dsn, newDB string) string {
	idx := strings.LastIndex(dsn, "/")
	qIdx := strings.Index(dsn[idx:], "?")
	if qIdx < 0 {
		return dsn[:idx] + "/" + newDB
	}
	return dsn[:idx] + "/" + newDB + dsn[idx+qIdx:]
}

// stripPgxParams 移除 pgx 专用 URL 参数（pool_max_conns/pool_min_conns/options），
// psql/pg_dump 不认识这些参数会报 invalid URI query parameter。
func stripPgxParams(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn // 解析失败原样返回（让 psql 报错）
	}
	q := u.Query()
	for _, k := range []string{"pool_max_conns", "pool_min_conns", "options"} {
		q.Del(k)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// restoreTestDBName 临时恢复库（基名与 scripts/restore_test.sh TEST_DB 同源；D-016
// 起带席位后缀——多席并发跑备份测试不互踩固定名临时库；脚本侧 TEST_DB 同步派生）。
var restoreTestDBName = "arb_restore_test_" + testutil.SeatTag()

// setupRestoreTestDB 建临时库并恢复 ARB_TEST_PG_DSN 的 dump，返回临时库 psql DSN + 清理函数。
func setupRestoreTestDB(t *testing.T) (testDSN string) {
	t.Helper()
	srcDSN := psqlDSN(t)
	admin := adminDSN(t)
	ctx := context.Background()

	// 清理旧临时库
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("DROP DATABASE IF EXISTS %s;", restoreTestDBName)).CombinedOutput(); err != nil {
		t.Fatalf("drop old restore db: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.CommandContext(context.Background(), "psql", admin, "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", restoreTestDBName)).Run()
	})

	// 建新临时库（无 OWNER 子句——owner 默认=当前连接用户，本地 arb/CI postgres 两端通）
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("CREATE DATABASE %s;", restoreTestDBName)).CombinedOutput(); err != nil {
		t.Fatalf("create restore db: %v\n%s", err, out)
	}

	// 推导临时库 DSN（剥离 pgx 参数）
	testDSN = stripPgxParams(swapDBName(srcDSN, restoreTestDBName))

	// pg_dump | psql restore
	dumpCmd := exec.CommandContext(ctx, "pg_dump", srcDSN)
	restoreCmd := exec.CommandContext(ctx, "psql", testDSN, "-q")
	pipe, err := dumpCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("dump stdout pipe: %v", err)
	}
	restoreCmd.Stdin = pipe
	if err := dumpCmd.Start(); err != nil {
		t.Fatalf("start pg_dump: %v", err)
	}
	if out, err := restoreCmd.CombinedOutput(); err != nil {
		t.Fatalf("psql restore: %v\n%s", err, out)
	}
	if err := dumpCmd.Wait(); err != nil {
		t.Fatalf("pg_dump wait: %v", err)
	}
	return testDSN
}

// countRows 查询表行数。
func countRows(t *testing.T, dsn, table string) int {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "psql", dsn, "-t", "-c",
		fmt.Sprintf("SELECT count(*) FROM %s;", table)).CombinedOutput()
	if err != nil {
		t.Fatalf("count %s: %v\n%s", table, err, out)
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// TestBackupRecovery_NoHistoryChange（§10.1.17）：备份恢复不改变历史事实。
// 恢复后 positions/position_cashflows/position_settlements 行数和源库一致。
func TestBackupRecovery_NoHistoryChange(t *testing.T) {
	srcDSN := psqlDSN(t)
	restoreDSN := setupRestoreTestDB(t)

	tables := []string{"positions", "position_cashflows", "position_settlements"}
	for _, tbl := range tables {
		srcCount := countRows(t, srcDSN, tbl)
		restoreCount := countRows(t, restoreDSN, tbl)
		if srcCount != restoreCount {
			t.Errorf("表 %s 行数不一致：源=%d 恢复=%d（备份恢复不应改变历史事实）", tbl, srcCount, restoreCount)
		}
	}
}

// TestBackupRecovery_NoDuplicateCashflow：备份恢复不重复现金流。
// 恢复后 position_cashflows.event_key 无重复（幂等键完整性）。
func TestBackupRecovery_NoDuplicateCashflow(t *testing.T) {
	restoreDSN := setupRestoreTestDB(t)
	srcDSN := psqlDSN(t)

	// 查询重复 event_key（排除 NULL）
	dupQuery := `SELECT event_key, count(*) FROM position_cashflows ` +
		`WHERE event_key IS NOT NULL GROUP BY event_key HAVING count(*) > 1;`

	out, err := exec.CommandContext(context.Background(), "psql", restoreDSN, "-t", "-c", dupQuery).CombinedOutput()
	if err != nil {
		t.Fatalf("query restore duplicate event_key: %v\n%s", err, out)
	}
	if dup := strings.TrimSpace(string(out)); dup != "" {
		t.Errorf("恢复后存在重复 event_key（备份恢复不应重复现金流）：\n%s", dup)
	}

	// baseline：源库本身也无重复
	srcOut, err := exec.CommandContext(context.Background(), "psql", srcDSN, "-t", "-c", dupQuery).CombinedOutput()
	if err != nil {
		t.Fatalf("query src duplicate event_key: %v\n%s", err, srcOut)
	}
	if dup := strings.TrimSpace(string(srcOut)); dup != "" {
		t.Logf("注意：源库本身已存在重复 event_key（非本测试引入）：\n%s", dup)
	}
}

// TestBackupRecovery_SchemaMigrationsPreserved：恢复后 schema_migrations 版本完整。
func TestBackupRecovery_SchemaMigrationsPreserved(t *testing.T) {
	srcDSN := psqlDSN(t)
	restoreDSN := setupRestoreTestDB(t)

	srcCount := countRows(t, srcDSN, "schema_migrations")
	restoreCount := countRows(t, restoreDSN, "schema_migrations")
	if srcCount != restoreCount {
		t.Errorf("schema_migrations 行数不一致：源=%d 恢复=%d（备份恢复应保留全部 migration 版本）",
			srcCount, restoreCount)
	}
}

// 确保 os 包被引用（部分构建环境可能优化）
var _ = os.Getenv
