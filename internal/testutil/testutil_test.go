package testutil

import (
	"os"
	"strings"
	"testing"
)

// setEnv 设置并注册清理（测试隔离）。
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

// assertTerminated 在独立 goroutine 中调用 fn（fn 内含 Fatalf/Skipf 调用），
// 断言 goroutine 终止（Fatalf/Skipf 调用 runtime.Goexit 终止 goroutine）。
// 返回 st 的 Failed/Skipped 状态供调用方进一步断言。
func assertTerminated(t *testing.T, fn func(st *testing.T)) (failed, skipped bool) {
	t.Helper()
	st := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(st)
	}()
	<-done
	return st.Failed(), st.Skipped()
}

// D-207 S1 单测：空 DSN→FAIL / 逃生门→SKIP / 无参 DSN→三参数全追加 /
// 含 ?pool_max_conns=2 → pool 不追加但 lock_timeout 追加 / 已含 options= → 不追加 /
// DSN 指向非 test 库→拒绝。
func TestTestDSN_EmptyDSN_Fatalf(t *testing.T) {
	setEnv(t, "ARB_TEST_PG_DSN", "")
	setEnv(t, "ARB_TEST_ALLOW_SKIP_DSN", "")
	failed, _ := assertTerminated(t, func(st *testing.T) {
		TestDSN(st, "ARB_TEST_PG_DSN")
	})
	if !failed {
		t.Fatal("空 DSN 应 Fatalf（Failed），但未 Failed")
	}
}

func TestTestDSN_AllowSkip_Skipf(t *testing.T) {
	setEnv(t, "ARB_TEST_PG_DSN", "")
	setEnv(t, "ARB_TEST_ALLOW_SKIP_DSN", "1")
	_, skipped := assertTerminated(t, func(st *testing.T) {
		TestDSN(st, "ARB_TEST_PG_DSN")
	})
	if !skipped {
		t.Fatal("逃生门+空 DSN 应 Skipf（Skipped），但未 Skipped")
	}
}

func TestNormalizeDSN_NoParams_AllAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test"
	got := normalizeDSN(dsn)
	for _, p := range []string{"pool_max_conns=4", "pool_min_conns=0", "options=-c%20lock_timeout=5s"} {
		if !strings.Contains(got, p) {
			t.Errorf("无参 DSN 应追加 %q，got=%s", p, got)
		}
	}
}

func TestNormalizeDSN_ExistingPool_LockTimeoutAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test?pool_max_conns=2"
	got := normalizeDSN(dsn)
	if strings.Contains(got, "pool_max_conns=4") {
		t.Errorf("已有 pool_max_conns=2 不应再追加 =4，got=%s", got)
	}
	if !strings.Contains(got, "pool_min_conns=0") {
		t.Errorf("缺 pool_min_conns 应追加，got=%s", got)
	}
	if !strings.Contains(got, "options=-c%20lock_timeout=5s") {
		t.Errorf("缺 options 应追加 lock_timeout，got=%s", got)
	}
}

func TestNormalizeDSN_ExistingOptions_NotAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test?options=-c%20lock_timeout=10s"
	got := normalizeDSN(dsn)
	if strings.Count(got, "options=") != 1 {
		t.Errorf("已有 options 不应再追加，got=%s（options= 出现 %d 次）", got, strings.Count(got, "options="))
	}
	if !strings.Contains(got, "pool_max_conns=4") {
		t.Errorf("缺 pool_max_conns 应追加，got=%s", got)
	}
}

func TestTestDSN_NonTestDB_Rejected(t *testing.T) {
	setEnv(t, "ARB_TEST_PG_DSN", "postgres://arb:arb@localhost:5435/arb")
	setEnv(t, "ARB_TEST_ALLOW_SKIP_DSN", "")
	failed, _ := assertTerminated(t, func(st *testing.T) {
		TestDSN(st, "ARB_TEST_PG_DSN")
	})
	if !failed {
		t.Fatal("指向非 test 库应 Fatalf（Failed），但未 Failed")
	}
}
