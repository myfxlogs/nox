package pgstore

import (
	"strings"
	"testing"
)

// TestSplitStatements（D-072 复审补充）：0034 起 migration 含 DO $$ ... $$ 块，
// 块内分号不得分割（施工期扩展 splitStatements 的回归保护）。
func TestSplitStatements(t *testing.T) {
	sql := `CREATE TABLE a (id int);
DO $$ DECLARE c text; BEGIN
  FOR c IN SELECT conname FROM pg_constraint LOOP
    EXECUTE format('ALTER TABLE a DROP CONSTRAINT %I', c); END LOOP;
END $$;
INSERT INTO a VALUES (1)`
	stmts := splitStatements(sql)
	if len(stmts) != 3 {
		t.Fatalf("want 3 statements, got %d: %q", len(stmts), stmts)
	}
	if !strings.HasPrefix(strings.TrimSpace(stmts[1]), "DO $$") {
		t.Errorf("stmt[1] should be the DO block, got %q", stmts[1])
	}
	if !strings.Contains(stmts[1], "END LOOP") {
		t.Errorf("DO block body was split (internal semicolons leaked): %q", stmts[1])
	}
}

// TestSplitStatementsPlain 既有无块迁移不受影响（分号分割 + 尾部无分号语句保留）。
func TestSplitStatementsPlain(t *testing.T) {
	stmts := splitStatements("UPDATE a SET x=1;\nUPDATE b SET y=2")
	if len(stmts) != 2 {
		t.Fatalf("want 2 statements, got %d: %q", len(stmts), stmts)
	}
	if strings.TrimSpace(stmts[0]) != "UPDATE a SET x=1" {
		t.Errorf("stmt[0] mismatch: %q", stmts[0])
	}
}
