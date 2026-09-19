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
func testStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(context.Background(), "TRUNCATE investment_records, position_daily_income CASCADE"); err != nil {
		t.Fatalf("TRUNCATE investment_records, position_daily_income: %v", err)
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

// seedRecords 插入 5 条覆盖三 kind + 多 asset 的记录。
func seedRecords(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	rows := []struct{ kind, asset, content string }{
		{"decision", "TRX", "质押 14 天解绑，能量收益观察"},
		{"lesson", "TRX", "强平底线 0.3695，跌破手动离场"},
		{"observation", "USDT", "资金费率转正，机会窗口"},
		{"decision", "BTC", "减仓一半，锁定利润"},
		{"lesson", "USDT", "滑点吃掉利润，市价单慎用"},
	}
	for _, r := range rows {
		if _, err := s.AddInvestmentRecord(ctx, r.kind, r.asset, r.content); err != nil {
			t.Fatalf("seed AddInvestmentRecord(%s,%s): %v", r.kind, r.asset, err)
		}
	}
}

// TestAddInvestmentRecord_ValidWrite：合法写入 → 返回 id + List 可读回字段全对（含 ts/updated_at 非零）。
func TestAddInvestmentRecord_ValidWrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	id, err := s.AddInvestmentRecord(ctx, "decision", "TRX", "质押观察：能量收益跑赢持币")
	if err != nil {
		t.Fatalf("AddInvestmentRecord: %v", err)
	}
	if id <= 0 {
		t.Errorf("returned id want > 0, got %d", id)
	}

	all, err := s.ListInvestmentRecords(ctx, "", "", "", 200, 0)
	if err != nil {
		t.Fatalf("ListInvestmentRecords: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 record, got %d", len(all))
	}
	r := all[0]
	if r.ID != id || r.Kind != "decision" || r.Asset != "TRX" || r.Content != "质押观察：能量收益跑赢持币" {
		t.Errorf("roundtrip mismatch: %+v", r)
	}
	if r.Ts.IsZero() || r.UpdatedAt.IsZero() {
		t.Errorf("ts/updated_at want non-zero, got %v/%v", r.Ts, r.UpdatedAt)
	}
}

// TestListInvestmentRecords_Filters：kind/asset/q 各过滤维度 + 组合 + 空筛选全量。
func TestListInvestmentRecords_Filters(t *testing.T) {
	s := testStore(t)
	seedRecords(t, s)
	ctx := context.Background()

	// 空筛选：全量 5 条
	all, err := s.ListInvestmentRecords(ctx, "", "", "", 200, 0)
	if err != nil {
		t.Fatalf("List empty filter: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("empty filter want 5, got %d", len(all))
	}

	// kind 过滤
	dec, err := s.ListInvestmentRecords(ctx, "decision", "", "", 200, 0)
	if err != nil {
		t.Fatalf("List by kind: %v", err)
	}
	if len(dec) != 2 {
		t.Errorf("kind=decision want 2, got %d", len(dec))
	}
	for _, r := range dec {
		if r.Kind != "decision" {
			t.Errorf("kind filter leaked %q", r.Kind)
		}
	}

	// asset 子串（ILIKE 不区分大小写：小写 "trx" 命中大写 "TRX"）
	trx, err := s.ListInvestmentRecords(ctx, "", "trx", "", 200, 0)
	if err != nil {
		t.Fatalf("List by asset: %v", err)
	}
	if len(trx) != 2 {
		t.Errorf("asset sub 'trx' want 2, got %d", len(trx))
	}
	for _, r := range trx {
		if r.Asset != "TRX" {
			t.Errorf("asset filter leaked %q", r.Asset)
		}
	}

	// q 内容子串
	byQ, err := s.ListInvestmentRecords(ctx, "", "", "离场", 200, 0)
	if err != nil {
		t.Fatalf("List by q: %v", err)
	}
	if len(byQ) != 1 {
		t.Errorf("q '离场' want 1, got %d", len(byQ))
	}
	if len(byQ) > 0 && byQ[0].Content != "强平底线 0.3695，跌破手动离场" {
		t.Errorf("q hit wrong record: %+v", byQ[0])
	}

	// 组合：kind=lesson + asset=USDT → 1 条
	combo, err := s.ListInvestmentRecords(ctx, "lesson", "USDT", "", 200, 0)
	if err != nil {
		t.Fatalf("List combo: %v", err)
	}
	if len(combo) != 1 {
		t.Errorf("combo lesson+USDT want 1, got %d", len(combo))
	}
	if len(combo) > 0 && combo[0].Content != "滑点吃掉利润，市价单慎用" {
		t.Errorf("combo hit wrong record: %+v", combo[0])
	}
}

// TestListInvestmentRecords_LimitOffset：分页语义（时间倒序，limit/offset 生效）。
func TestListInvestmentRecords_LimitOffset(t *testing.T) {
	s := testStore(t)
	seedRecords(t, s)
	ctx := context.Background()

	// 错开 ts 使排序确定：id 越大 ts 越晚 → ts DESC = id 倒序。
	if _, err := s.pool.Exec(ctx, `UPDATE investment_records SET ts = now() + (id * interval '1 second')`); err != nil {
		t.Fatalf("stagger ts: %v", err)
	}

	all, err := s.ListInvestmentRecords(ctx, "", "", "", 200, 0)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("want 5, got %d", len(all))
	}
	// id 基准从实际返回集取（TRUNCATE 不重置 IDENTITY 序列，id 非 1 起始）。
	ids := make([]int64, len(all))
	for i, r := range all {
		ids[i] = r.ID
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Errorf("ts DESC (id DESC) order broken at %d: ids %v", i, ids)
			break
		}
	}

	// limit 2 → 前 2 条
	lim, err := s.ListInvestmentRecords(ctx, "", "", "", 2, 0)
	if err != nil {
		t.Fatalf("List limit: %v", err)
	}
	if len(lim) != 2 || lim[0].ID != ids[0] || lim[1].ID != ids[1] {
		t.Errorf("limit 2 want ids [%d,%d], got %d items [%d,%d]", ids[0], ids[1], len(lim), lim[0].ID, lim[1].ID)
	}

	// offset 2 limit 2 → 跳过前 2，取第 3、4 条
	pg, err := s.ListInvestmentRecords(ctx, "", "", "", 2, 2)
	if err != nil {
		t.Fatalf("List offset: %v", err)
	}
	if len(pg) != 2 || pg[0].ID != ids[2] || pg[1].ID != ids[3] {
		t.Errorf("offset 2 limit 2 want ids [%d,%d], got %d items [%d,%d]", ids[2], ids[3], len(pg), pg[0].ID, pg[1].ID)
	}

	// limit ≤ 0 → 默认 200（仍返回全量 5）
	def, err := s.ListInvestmentRecords(ctx, "", "", "", 0, 0)
	if err != nil {
		t.Fatalf("List default limit: %v", err)
	}
	if len(def) != 5 {
		t.Errorf("limit 0 (default 200) want 5, got %d", len(def))
	}
}
