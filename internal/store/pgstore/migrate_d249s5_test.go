package pgstore

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"arb/internal/testutil"
)

// D-249 S5 批4 存储三项：per-migration 事务 + CreatePosition 链原子性测试。
// 先红后绿纪律（D-207 loud-FAIL）：每断言先给红证据再给绿证据。

// TestMigrate_PerMigrationTx_HalfFailureReentry D-249 S5 审计 P1-6：
// per-migration 事务包裹后，中途失败的迁移可重入——不留半途残留，重跑无错误。
// 先红：构造一个含"成功语句+失败语句"的 migration，旧代码（autocommit）下成功语句残留；
// 后绿：新代码（事务包裹）下全部回滚，schema_migrations 不记录该版本，重跑可重入。
func TestMigrate_PerMigrationTx_HalfFailureReentry(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	const halfFailDB = "arb_migrate_halffail_test"

	// 清理旧库
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("DROP DATABASE IF EXISTS %s;", halfFailDB)).CombinedOutput(); err != nil {
		t.Fatalf("drop old half-fail db: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.CommandContext(context.Background(), "psql", admin, "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", halfFailDB)).Run()
	})

	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("CREATE DATABASE %s;", halfFailDB)).CombinedOutput(); err != nil {
		t.Fatalf("create half-fail db: %v\n%s", err, out)
	}

	halfFailDSN := swapDBName(testutil.TestDSN(t, "ARB_TEST_PG_DSN"), halfFailDB)
	s, err := New(ctx, halfFailDSN)
	if err != nil {
		t.Fatalf("pgstore.New on half-fail db: %v", err)
	}
	defer s.Close()

	// 建 schema_migrations（Migrate 入口会建，但我们需要先建以手动控制版本记录）。
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	// 模拟一个半途失败的 migration：第一条 CREATE TABLE 成功，第二条引用不存在的表→失败。
	// 旧代码（autocommit）：CREATE TABLE 残留，失败后 schema_migrations 不记录 → 重跑时
	//   splitStatements 第一条再跑（CREATE TABLE IF NOT EXISTS 幂等）→ 第二条再失败 → 永远卡死。
	// 新代码（事务）：CREATE TABLE 回滚 → 重跑时全量重试 → 第二条仍失败（预期，证明事务回滚生效）。
	halfFailSQL := `CREATE TABLE IF NOT EXISTS half_fail_marker (id int);
		SELECT nonexistent_column FROM nonexistent_table_d249s5`

	// 先红证据：旧代码路径（直接 pool.Exec 逐语句）下，第一条会残留。
	t.Run("red_old_autocommit_leaves_residue", func(t *testing.T) {
		// 逐语句 autocommit（模拟旧 migrate.go 行为；D-508 S6 分割器拆除后测试内联 Split）。
		for _, stmt := range strings.Split(halfFailSQL, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			_, _ = s.pool.Exec(ctx, stmt) // 故意忽略错误=旧 autocommit 行为
		}
		// 旧代码下 half_fail_marker 表残留（半途残留=先红证据）。
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name='half_fail_marker')`).Scan(&exists)
		if err != nil {
			t.Fatalf("check marker table: %v", err)
		}
		if !exists {
			t.Fatal("red 证据缺失：旧 autocommit 应残留 half_fail_marker 表（若不存在说明测试前提不成立）")
		}
		// 清理残留，为绿证准备干净库。
		_, _ = s.pool.Exec(ctx, `DROP TABLE IF EXISTS half_fail_marker`)
	})

	// 后绿证据：新代码（applyMigrationTx 事务包裹）下，失败后不留残留。
	t.Run("green_tx_no_residue", func(t *testing.T) {
		err := s.applyMigrationTx(ctx, 99990, halfFailSQL)
		if err == nil {
			t.Fatal("applyMigrationTx 应返回错误（第二条 SQL 引用不存在的表）")
		}
		// 绿证：half_fail_marker 表不应存在（事务回滚）。
		var exists bool
		err = s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name='half_fail_marker')`).Scan(&exists)
		if err != nil {
			t.Fatalf("check marker table after tx: %v", err)
		}
		if exists {
			t.Errorf("green 证失败：事务回滚后 half_fail_marker 表不应存在（残留=事务未生效）")
		}
		// 绿证：schema_migrations 不记录版本 99990（事务回滚=版本不记录）。
		var recorded int
		_ = s.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version=99990`).Scan(&recorded)
		if recorded == 1 {
			t.Errorf("green 证失败：失败迁移的版本号不应记录在 schema_migrations")
		}
	})

	// 重入证：重跑 applyMigrationTx 仍失败（第二条 SQL 仍引用不存在的表），
	// 但不留新残留（事务回滚一致），证明可重入（不会因残留而卡死或行为变化）。
	t.Run("reentry_same_failure_no_residue", func(t *testing.T) {
		err := s.applyMigrationTx(ctx, 99990, halfFailSQL)
		if err == nil {
			t.Fatal("重入：applyMigrationTx 应仍返回错误")
		}
		var exists bool
		_ = s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name='half_fail_marker')`).Scan(&exists)
		if exists {
			t.Errorf("重入后不应残留 half_fail_marker（事务回滚应一致生效）")
		}
	})
}

// TestCreatePositionWithLegs_Atomicity D-249 S5 审计 P1-8：
// CreatePosition + Legs 事务包裹后，leg 写入失败 → position 行不留孤儿（整体回滚）。
// 先红：旧代码（autocommit CreatePosition + CreatePositionLeg）下，position 行残留；
// 后绿：新代码（CreatePositionWithLegs 事务）下，leg 冲突 → position 行回滚不留孤儿。
func TestCreatePositionWithLegs_Atomicity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// 构造两条 leg 共享 (leg_no=0, phase="OPEN", attempt_no=1) → 第二条 ON CONFLICT DO NOTHING
	// → 0 行 → ErrTerminalState → 事务回滚。
	dupLegA := &PositionLeg{
		LegNo: 0, Phase: "OPEN", AttemptNo: 1,
		Venue: "test:mt5", Symbol: "EURUSD", Side: "BUY",
		Qty: 0.1, PriceExpected: 1.1, Status: "FILLED",
	}
	dupLegB := &PositionLeg{
		LegNo: 0, Phase: "OPEN", AttemptNo: 1, // 同 (leg_no, phase, attempt_no) → 冲突
		Venue: "test:spot", Symbol: "EURUSDT", Side: "SELL",
		Qty: 0.1, PriceExpected: 1.1, Status: "FILLED",
	}

	pos := &Position{
		OpportunityID: "opp-d249s5-atomic",
		Type:          "CROSS_VENUE",
		Status:        "OPENING",
	}

	// 后绿：CreatePositionWithLegs 事务包裹 → leg 冲突 → ErrTerminalState + position 回滚。
	id, err := s.CreatePositionWithLegs(ctx, pos, []*PositionLeg{dupLegA, dupLegB})
	if err == nil {
		t.Fatalf("CreatePositionWithLegs 应返回错误（leg 冲突→ErrTerminalState），got id=%d", id)
	}
	if !strings.Contains(err.Error(), "terminal state") {
		t.Errorf("错误应含 ErrTerminalState 语义，got: %v", err)
	}

	// 绿证：position 行不应存在（事务回滚=无孤儿）。
	var count int
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE opportunity_id='opp-d249s5-atomic'`).Scan(&count)
	if err != nil {
		t.Fatalf("query position count: %v", err)
	}
	if count != 0 {
		t.Errorf("green 证失败：事务回滚后 position 行不应存在，got %d 行（孤儿=事务未生效）", count)
	}

	// 绿证：position_legs 行不应存在。
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM position_legs WHERE venue_key IN ('test:mt5','test:spot')`).Scan(&count)
	if err != nil {
		t.Fatalf("query leg count: %v", err)
	}
	if count != 0 {
		t.Errorf("green 证失败：事务回滚后 position_legs 不应存在，got %d 行", count)
	}
}

// TestCreatePositionWithLegs_Success D-249 S5：正常路径——CreatePosition + Legs 全部成功 → 事务提交。
func TestCreatePositionWithLegs_Success(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	legs := []*PositionLeg{
		{LegNo: 0, Phase: "OPEN", AttemptNo: 1, Venue: "test:mt5", Symbol: "EURUSD",
			Side: "BUY", Qty: 0.1, PriceExpected: 1.1, Status: "FILLED"},
		{LegNo: 1, Phase: "OPEN", AttemptNo: 1, Venue: "test:spot", Symbol: "EURUSDT",
			Side: "SELL", Qty: 0.1, PriceExpected: 1.1, Status: "FILLED"},
	}
	pos := &Position{
		OpportunityID: "opp-d249s5-success",
		Type:          "CROSS_VENUE",
		Status:        "OPENING",
	}

	id, err := s.CreatePositionWithLegs(ctx, pos, legs)
	if err != nil {
		t.Fatalf("CreatePositionWithLegs 正常路径应成功: %v", err)
	}
	if id <= 0 {
		t.Fatalf("返回 id 应 >0，got %d", id)
	}

	// 验证 position 行存在。
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE id=$1 AND opportunity_id='opp-d249s5-success'`, id).Scan(&count); err != nil {
		t.Fatalf("query position: %v", err)
	}
	if count != 1 {
		t.Errorf("position 行应存在 1 行，got %d", count)
	}

	// 验证 legs 行存在 2 行且 PositionID 正确。
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM position_legs WHERE position_id=$1`, id).Scan(&count); err != nil {
		t.Fatalf("query legs: %v", err)
	}
	if count != 2 {
		t.Errorf("position_legs 应存在 2 行，got %d", count)
	}

	// 验证 legs 的 PositionID 被回填。
	for _, leg := range legs {
		if leg.PositionID != id {
			t.Errorf("leg PositionID 应被回填为 %d，got %d", id, leg.PositionID)
		}
	}
}

// TestCreatePositionWithLegs_EmptyLegs D-249 S5：legs 为空时等价于 CreatePosition（仅建 position 行）。
func TestCreatePositionWithLegs_EmptyLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	pos := &Position{
		OpportunityID: "opp-d249s5-empty-legs",
		Type:          "CROSS_VENUE",
		Status:        "OPENING",
	}
	id, err := s.CreatePositionWithLegs(ctx, pos, nil)
	if err != nil {
		t.Fatalf("CreatePositionWithLegs 空 legs 应成功: %v", err)
	}
	if id <= 0 {
		t.Fatalf("返回 id 应 >0，got %d", id)
	}

	var posCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE id=$1`, id).Scan(&posCount); err != nil {
		t.Fatalf("query position: %v", err)
	}
	if posCount != 1 {
		t.Errorf("position 行应存在 1 行，got %d", posCount)
	}
}
