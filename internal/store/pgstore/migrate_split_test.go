package pgstore

import (
	"context"
	"strings"
	"testing"
)

// TestMigration_MultiStmtAndDOBlock（D-508 S6 改造）：分割器拆除后，pgx simple
// protocol 整批执行仍须正确处理含 `;` 的 `--` 注释、'...' 字符串与 DO $$ 块——
// 解析责任移交 PostgreSQL 本体（比自研分割器更正确），本测试钉死回归面。
func TestMigration_MultiStmtAndDOBlock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	sql := `-- 注释内含分号;不得截断
CREATE TABLE IF NOT EXISTS d508_split_probe (id int, note text);
INSERT INTO d508_split_probe (id, note) VALUES (1, 'semi;colon');
DO $$ DECLARE c text; BEGIN
  FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='d508_split_probe'::regclass LOOP
    EXECUTE format('x;%s', c);
  END LOOP;
END $$;
UPDATE d508_split_probe SET note='done' WHERE id=1;`
	if err := s.applyMigrationTx(ctx, 99991, sql); err != nil {
		t.Fatalf("多语句+DO$$ 迁移应整体成功: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DROP TABLE IF EXISTS d508_split_probe`)
	})
	var note string
	if err := s.pool.QueryRow(ctx, `SELECT note FROM d508_split_probe WHERE id=1`).Scan(&note); err != nil || note != "done" {
		t.Fatalf("尾语句未生效: note=%q err=%v", note, err)
	}
	var recorded bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=99991)`).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("版本未记录: %v", err)
	}
	if !strings.Contains(sql, "DO $$") {
		t.Fatal("fixture 自检")
	}
}
