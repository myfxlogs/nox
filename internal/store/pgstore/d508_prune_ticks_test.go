// d508_prune_ticks_test.go —— D-508 S4b：PruneTicks 分区级 90d 裁剪（DSN 门控）。
package pgstore

import (
	"context"
	"testing"
)

func TestD508_PruneTicksPartitionDrop(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()

	// 构造窗内（前 3 个月边缘）+窗外分区与数据。
	must := func(q string, args ...any) {
		if _, err := s.pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	must(`CREATE TABLE IF NOT EXISTS ticks_2020_01 PARTITION OF ticks
		FOR VALUES FROM ('2020-01-01') TO ('2020-02-01')`)
	defer s.pool.Exec(ctx, `DROP TABLE IF EXISTS ticks_2020_01`)
	must(`CREATE TABLE IF NOT EXISTS ticks_2030_01 PARTITION OF ticks
		FOR VALUES FROM ('2030-01-01') TO ('2030-02-01')`)
	defer s.pool.Exec(ctx, `DROP TABLE IF EXISTS ticks_2030_01`)
	must(`INSERT INTO ticks (ts, venue, symbol, bid, ask, venue_type)
		VALUES ('2020-01-15', 'x', 'y', 1, 1, 'perp'), ('2030-01-15', 'x', 'y', 1, 1, 'perp')`)
	defer s.pool.Exec(ctx, `DELETE FROM ticks WHERE venue='x'`)

	n, err := s.PruneTicks(ctx)
	if err != nil {
		t.Fatalf("PruneTicks: %v", err)
	}
	if n != 1 {
		t.Fatalf("dropped=%d want 1", n)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname='ticks_2020_01')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("ticks_2020_01 应被 DROP")
	}
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname='ticks_2030_01')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("ticks_2030_01 窗内应保留")
	}
	var cnt int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ticks WHERE venue='x'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("ticks 行=%d want 1（2030 行存、2020 行随分区删）", cnt)
	}
}
