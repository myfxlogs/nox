package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
)

// serviceTestStoreSingleInit D-351 S1：改走 storetest.SharedStore 共享池（原 D-212/D-234
// 本包手搓单例的泛化收编——机制上移 testutil，dial 数进程级 O(1)）。
func serviceTestStoreSingleInit(t *testing.T) *pgstore.Store {
	t.Helper()
	return storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
}

// testServiceStore 连接 ARB_TEST_PG_SERVICE_DSN 指定的专用测试库（与 pgstore 包测试库隔离，避免并行 TRUNCATE 竞态）。
// DSN 缺失 loud-FAIL + _test 库守卫由 testutil.TestDSN 承担（D-234 批 1）。
// D-212 CI 红根因修复：改为包级单例 pool（serviceTestStoreSingleInit），避免每测试 New 独立 pool
// 累积连接耗尽 pg 服务端 max_connections → Acquire 卡住 → 30s 超时 flaky。
// D-120：原 investment_records 清表随表清退移除。
func testServiceStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	// 每测试函数清财务线表（数据隔离；schema 重建每进程仅一次）。
	truncCtx, truncCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer truncCancel()
	if _, err := s.Pool().Exec(truncCtx, "TRUNCATE position_daily_income, personal_positions CASCADE"); err != nil {
		t.Fatalf("TRUNCATE position_daily_income, personal_positions: %v", err)
	}
	return s
}
