package pgstore

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// testStore 连接 ARB_TEST_PG_DSN 指定的专用测试库（可销毁数据）。
// 未设置时跳过：go test ./... 无 PG 环境仍全过。
// 安全闸（ref/arbcn 同款教训）：测试会对目标表 TRUNCATE，绝不允许指向非测试库——dbname 必须含 "test"。
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ARB_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_DSN 未设置，跳过需真库的测试")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse ARB_TEST_PG_DSN: %v", err)
	}
	if !strings.Contains(cfg.Database, "test") {
		t.Fatalf("ARB_TEST_PG_DSN 指向非测试库 %q：测试会 TRUNCATE 该库，拒绝执行", cfg.Database)
	}
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	ensureBaseApplied(t, ctx, s)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "TRUNCATE investment_records, position_daily_income CASCADE"); err != nil {
		t.Fatalf("TRUNCATE investment_records, position_daily_income: %v", err)
	}
	return s
}

// ensureBaseApplied 使新鲜测试库与生产库同态：base schema（v1..N-1）标记已应用，Migrate 只跑被测的最后一个版本（v7）。
// 背景：0001_init.sql 内部也会建 schema_migrations（无 IF NOT EXISTS），与 migrate.go 外层创建冲突（存量问题，
// 生产库因 base 已应用而从不重跑 0001、不触发）；测试库为新鲜库，须先预标记 base 版本才能走通 Migrate。
func ensureBaseApplied(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	// 迁移是追加式（v1..N）；预标记除最后一个（被测迁移）外全部版本。
	for _, m := range migrationFiles[:len(migrationFiles)-1] {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, m.version,
		); err != nil {
			t.Fatalf("mark migration v%d applied: %v", m.version, err)
		}
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
