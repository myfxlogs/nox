// shared_test.go D-351 S1：pgstore 包内共享测试 Store（进程级单例，sync.Once 语义）。
// 不直接复用 testutil/storetest.SharedStore 的原因：in-package 测试引用 storetest→pgstore
// 构成 Go 禁止的 import cycle——机制就地实现（同款：同 DSN 返回同指针=进程级 O(1) dial；
// 数据隔离仍由各测试既有 TRUNCATE/前缀 DELETE 自持；池不随单测试关闭）。
package pgstore

import (
	"context"
	"sync"
	"testing"

	"arb/internal/testutil"
)

var (
	sharedTestMu   sync.Mutex
	sharedTestDsn  string
	sharedTestInst *Store
)

func sharedTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	sharedTestMu.Lock()
	defer sharedTestMu.Unlock()
	if sharedTestInst == nil || sharedTestDsn != dsn {
		s, err := New(context.Background(), dsn)
		if err != nil {
			t.Fatalf("shared test store: %v", err)
		}
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("shared test store migrate: %v", err)
		}
		sharedTestDsn, sharedTestInst = dsn, s
	}
	// 每次获取都复位易变 flags（缓存命中路径同样要过——泄漏中性化必须在获取口）
	return sharedTestInst.resetVolatileFlagsForSharedTest()
}

// resetVolatileFlagsForSharedTest 共享 Store 的跨测试泄漏中性化：alertPaused/alertHook
// 是实例级易变开关（per-test 池时代随实例自灭），共享后必须在每次获取时复位——
// 需要的测试在自己的测试体内重新设置（如 opportunities_alert_test）。
func (s *Store) resetVolatileFlagsForSharedTest() *Store {
	s.SetAlertPaused(false)
	s.SetAlertHook(nil)
	return s
}
