package pgstore

// migrate_registry_test.go —— D-207 返修门禁洞：迁移文件在仓未注册无任何一道拦。
// 对拍方向=文件→注册表：migrations/ 下每个 NNNN_*.sql 必须在 migrationFiles 有对应版本行，
// 否则文件永不执行（0200 实证：在仓但生产 schema_migrations max=199）。

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationFiles_RepoCoverage(t *testing.T) {
	entries, err := os.ReadDir("../../../migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	registered := make(map[int]bool, len(migrationFiles))
	for _, m := range migrationFiles {
		if registered[m.version] {
			t.Errorf("migrationFiles 版本 %d 重复注册", m.version)
		}
		registered[m.version] = true
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if len(name) < 6 || name[4] != '_' {
			t.Fatalf("迁移文件名不合 NNNN_*.sql 契约: %s", name)
		}
		v, err := strconv.Atoi(name[:4])
		if err != nil {
			t.Fatalf("迁移文件名不合 NNNN_*.sql 契约: %s", name)
		}
		if !registered[v] {
			t.Errorf("migrations/%s 在仓但未注册 migrationFiles（版本 %d 永不执行）", name, v)
		}
	}
}
