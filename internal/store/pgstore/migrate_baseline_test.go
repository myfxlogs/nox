package pgstore

// migrate_baseline_test.go —— D-214 基线闸：1000_baseline.sql 保持可对空库重放
//（cmd/migrate 临时验证器删除后的等价门禁）；squash 取代语义锚（≥1000 行在→<1000 不判 pending）。

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"nox/internal/platform/testutil"
	"nox/migrations"
)

// TestBaseline_Replayable：baseline dump 对空库可重放+表数=84（生产 91−7 不搬表）。
func TestBaseline_Replayable(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	db := "nox_baseline_replay_test_" + testutil.SeatTag()
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
	if _, err := s.Pool().Exec(ctx, migrations.BaselineSQL); err != nil {
		t.Fatalf("baseline replay: %v", err)
	}
	// 85 CREATE TABLE（92−7 不搬表；D-512 +fin_manual_position）；information_schema 另含 v_cycle_realrate 视图
	// 与 pg_stat_statements 扩展两对象——口径=BASE TABLE 且剔扩展件。
	var n int
	if err := s.Pool().QueryRow(ctx, `
		SELECT COUNT(*)::int FROM information_schema.tables
		WHERE table_schema='public' AND table_type='BASE TABLE'
		  AND table_name NOT IN ('pg_stat_statements','pg_stat_statements_info')`).Scan(&n); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if n != 85 {
		t.Fatalf("baseline 表数=%d want 85（92−7 不搬表）", n)
	}
	for _, tbl := range []string{"fin_cmc_account_summary", "fin_cmc_history", "fin_cmc_positions",
		"fin_cmc_prices", "intra_events", "paper_intra_trades", "price_alerts"} {
		var ex bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`, tbl).Scan(&ex); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if ex {
			t.Fatalf("baseline 含不搬表 %s（§3 剔除违约）", tbl)
		}
	}
}

// TestBaseline_SquashSupersedes：schema_migrations 含 ≥1000 行时 <1000 不再判 pending
// （M3 切流 TRUNCATE+写 1000 后历史链不被重放）。
func TestBaseline_SquashSupersedes(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	db := "nox_baseline_squash_test_" + testutil.SeatTag()
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
		t.Fatalf("fresh migrate: %v", err)
	}
	// 鲜库全链重放后 baseline 自动补 marker（不执行 dump）。
	var n0 int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*)::int FROM schema_migrations WHERE version=1000`).Scan(&n0); err != nil {
		t.Fatalf("check marker: %v", err)
	}
	if n0 != 1 {
		t.Fatalf("鲜库 Migrate 后 baseline marker 应在：%d", n0)
	}
	// 模拟切流态：TRUNCATE 后只写 1000 marker——<1000 全取代不重放。
	if _, err := s.Pool().Exec(ctx, `TRUNCATE schema_migrations`); err != nil {
		t.Fatalf("truncate schema_migrations: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO schema_migrations (version) VALUES (1000)`); err != nil {
		t.Fatalf("seed baseline marker: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after baseline marker: %v", err)
	}
	// <1000 全取代零重放；≥1000 新迁移（如 1001 dept 枚举）照常应用。
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT COUNT(*)::int FROM schema_migrations WHERE version < 1000`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("baseline marker 后 <1000 应零执行：schema_migrations 含 %d 行旧版本", n)
	}
}
