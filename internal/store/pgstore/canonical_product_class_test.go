package pgstore

import (
	"context"
	"testing"
)

// DB 集成测试：ListCanonicalProductClasses（D-558 SWAPARB 配对等价键表）。
// 需 NOX_TEST_PG_DSN，未设置时 skip（testStore 门禁）。

func TestListCanonicalProductClasses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE fin_canonical_product_class`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO fin_canonical_product_class (canonical, product_class) VALUES
		('XAUUSD','metal'), ('BTCUSDT','crypto'), ('WTI','energy')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := s.ListCanonicalProductClasses(ctx)
	if err != nil {
		t.Fatalf("ListCanonicalProductClasses: %v", err)
	}
	want := map[string]string{"XAUUSD": "metal", "BTCUSDT": "crypto", "WTI": "energy"}
	if len(got) != len(want) {
		t.Fatalf("rows: got %d, want %d (%v)", len(got), len(want), got)
	}
	for c, w := range want {
		if got[c] != w {
			t.Errorf("%s: got %q, want %q", c, got[c], w)
		}
	}
}

// 迁移种子回归锚：1037 幂等种子后表非空（防「迁移落文件漏注册」假绿——
// 经验库 migration-file-without-embed-registration 同款病灶）。
func TestCanonicalProductClassMigrationSeeded(t *testing.T) {
	s := testStore(t)
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM fin_canonical_product_class`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Fatal("fin_canonical_product_class 空——迁移 1037 种子未生效（检查 embed 注册+Migrate 链）")
	}
}
