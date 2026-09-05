package cnfi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// DSN 门控落库测试（D-207 loud-FAIL；D-234 P0-1 教训：绝不绕过 TestDSN）。
// 共享 arb_test 库（B-1 债）：测试用哨兵日期 1970-01-02（真实数据永不含）+ defer 自清理。
const testDay = "1970-01-02"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	ms, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	if err := ms.Migrate(context.Background()); err != nil {
		ms.Close()
		t.Fatalf("migrate: %v", err)
	}
	ms.Close()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM cnfi_series WHERE as_of = $1`, testDay)
	})
	return pool
}

func TestUpsertSeries_IdempotentAndChanged(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cs := NewStore(pool)
	base := []Point{{AsOf: testDay, Series: "cgb", Tenor: "10y", ValueBP: 168.04}}
	w1, c1, err := cs.UpsertSeries(ctx, base, "chinabond", "test-run-1", time.Now().UTC())
	if err != nil || w1 != 1 || c1 != 0 {
		t.Fatalf("first insert: written=%d changed=%d err=%v (want 1/0/nil)", w1, c1, err)
	}
	// 同值重跑 = 幂等（冲突+WHERE 过滤 → ErrNoRows 路径 → written=0 changed=0）
	w2, c2, err := cs.UpsertSeries(ctx, base, "chinabond", "test-run-2", time.Now().UTC())
	if err != nil || w2 != 0 || c2 != 0 {
		t.Fatalf("idempotent rerun: written=%d changed=%d err=%v (want 0/0/nil)", w2, c2, err)
	}
	// 值修订 = changed
	rev := []Point{{AsOf: testDay, Series: "cgb", Tenor: "10y", ValueBP: 168.05}}
	w3, c3, err := cs.UpsertSeries(ctx, rev, "chinabond", "test-run-3", time.Now().UTC())
	if err != nil || w3 != 0 || c3 != 1 {
		t.Fatalf("revision: written=%d changed=%d err=%v (want 0/1/nil)", w3, c3, err)
	}
}

func TestUpsertSeries_InvalidTenorRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cs := NewStore(pool)
	bad := []Point{{AsOf: testDay, Series: "cgb", Tenor: "2.5y", ValueBP: 100}}
	_, _, err := cs.UpsertSeries(ctx, bad, "chinabond", "test-run-bad", time.Now().UTC())
	if err == nil {
		t.Fatal("invalid tenor must be rejected by CHECK constraint")
	}
	if !strings.Contains(err.Error(), "cnfi_series_tenor_check") {
		t.Fatalf("expected tenor CHECK violation, got: %v", err)
	}
}
