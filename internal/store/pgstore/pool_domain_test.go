package pgstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"nox/internal/platform/testutil"
)

// TestPoolDomainIsolation D-633 拆票锚：读面爆量（占满通用池）时钱径池延迟不受影响。
func TestPoolDomainIsolation(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Skipf("pool: %v", err)
	}
	defer st.Close()
	stMoney, err := NewMoneyStore(ctx, dsn)
	if err != nil {
		t.Skipf("money pool: %v", err)
	}
	defer stMoney.Close()

	// 通用池 10 连占满（慢查询持连 800ms——读面爆量模拟）
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = st.Pool().Exec(ctx, `SELECT pg_sleep(0.8)`)
		}()
	}
	// 等占满
	time.Sleep(50 * time.Millisecond)
	// 钱径池——独立小池不应被通用池慢连拖（pgxpool 独立 MaxConns 在案）
	start := time.Now()
	if _, err := stMoney.Pool().Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("money pool exec: %v", err)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("钱径池被读面拖慢 %v>400ms（隔离失败）", d)
	}
	wg.Wait()
}
