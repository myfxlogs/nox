package gate

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// testAccumStore 单测专用 store（复用 pgstore testStore 的 DSN 约定；
// 原件随 accumulator_test 留 autotrade——gate 域测试同型复刻，D-212 拆搬）。
func testAccumStore(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
