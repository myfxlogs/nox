// storetest_test.go D-351 T3 静态断言（S1 替代变异锚）：同 DSN 重复取共享 Store
// 必须同一指针——dial 数从每测试 O(n) 降为进程级 O(1)。
package storetest

import (
	"os"
	"testing"

	"arb/internal/testutil"
)

func TestSharedStore_SamePointerPerDSN(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	setEnv(t, "ARB_TEST_PG_DSN", dsn)
	a := SharedStore(t, "ARB_TEST_PG_DSN")
	b := SharedStore(t, "ARB_TEST_PG_DSN")
	if a != b {
		t.Error("同 DSN 两次 SharedStore 应返回同一 *pgstore.Store（共享池=O(1) dial），实得不同指针")
	}
}

// setEnv 设置并注册清理（与 testutil 主包同款；本包无法引用其未导出 helper）。
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	os.Setenv(key, value)
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}
