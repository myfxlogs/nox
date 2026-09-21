package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

// D-207 v1.1 单测：空 DSN→FAIL / 逃生门→SKIP / 无参 DSN→只追加 options /
// 已含 pool 参数 → 不追加 pool，只追加 options / 已含 options= → 不追加 /
// DSN 指向非 test 库→拒绝 / pool 参数不被追加（防回归）。
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

// TestNormalizeDSN_NoParams_OnlyOptionsAppended：无参 DSN 只追加 options（v1.1：不追加 pool 参数）。
func TestNormalizeDSN_NoParams_OnlyOptionsAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test"
	got := normalizeDSN(dsn)
	if !strings.Contains(got, "options=-c%20lock_timeout=5s") {
		t.Errorf("无参 DSN 应追加 options，got=%s", got)
	}
	if strings.Contains(got, "pool_max_conns=") {
		t.Errorf("v1.1 不应追加 pool_max_conns，got=%s", got)
	}
	if strings.Contains(got, "pool_min_conns=") {
		t.Errorf("v1.1 不应追加 pool_min_conns，got=%s", got)
	}
}

// TestNormalizeDSN_ExistingPool_LockTimeoutAppended：已有 pool 参数 → 只追加 options（v1.1：pool 不动）。
func TestNormalizeDSN_ExistingPool_LockTimeoutAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test?pool_max_conns=2"
	got := normalizeDSN(dsn)
	if !strings.Contains(got, "pool_max_conns=2") {
		t.Errorf("已有 pool_max_conns=2 应保留，got=%s", got)
	}
	if !strings.Contains(got, "options=-c%20lock_timeout=5s") {
		t.Errorf("缺 options 应追加 lock_timeout，got=%s", got)
	}
}

// TestNormalizeDSN_PoolParamsNotAppended：防回归——输出不含 pool_max_conns=/pool_min_conns=（v1.1）。
func TestNormalizeDSN_PoolParamsNotAppended(t *testing.T) {
	for _, dsn := range []string{
		"postgres://arb:arb@localhost:5435/arb_test",
		"postgres://arb:arb@localhost:5435/arb_test?sslmode=disable",
		"postgres://arb:arb@localhost:5435/arb_test?pool_max_conns=2",
	} {
		got := normalizeDSN(dsn)
		if strings.Contains(got, "pool_max_conns=") && !strings.Contains(dsn, "pool_max_conns=") {
			t.Errorf("normalizeDSN 不应追加 pool_max_conns（v1.1 防回归），input=%s got=%s", dsn, got)
		}
		if strings.Contains(got, "pool_min_conns=") && !strings.Contains(dsn, "pool_min_conns=") {
			t.Errorf("normalizeDSN 不应追加 pool_min_conns（v1.1 防回归），input=%s got=%s", dsn, got)
		}
	}
}

func TestNormalizeDSN_ExistingOptions_NotAppended(t *testing.T) {
	dsn := "postgres://arb:arb@localhost:5435/arb_test?options=-c%20lock_timeout=10s"
	got := normalizeDSN(dsn)
	if strings.Count(got, "options=") != 1 {
		t.Errorf("已有 options 不应再追加，got=%s（options= 出现 %d 次）", got, strings.Count(got, "options="))
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

// TestNormalizeDSN_ConnectTimeoutAppended D-351 T1（S2）：normalizeDSN 输出必须含
// connect_timeout=5——连接类故障 120s 挂死变 5s 快败（pgx/libpq 原生参数）。
func TestNormalizeDSN_ConnectTimeoutAppended(t *testing.T) {
	for _, dsn := range []string{
		"postgres://arb:arb@localhost:5435/arb_test",
		"postgres://arb:arb@localhost:5435/arb_test?sslmode=disable",
		"postgres://arb:arb@localhost:5435/arb_test?options=-c%20lock_timeout=10s",
	} {
		got := normalizeDSN(dsn)
		if strings.Count(got, "connect_timeout=5") != 1 {
			t.Errorf("normalizeDSN 应追加恰一个 connect_timeout=5，input=%s got=%s", dsn, got)
		}
		if strings.Count(got, "connect_timeout=") != 1 {
			t.Errorf("已有 connect_timeout 不应重复追加，input=%s got=%s", dsn, got)
		}
	}
	// 已有自定义 connect_timeout=3 → 原样保留不覆盖
	got := normalizeDSN("postgres://arb:arb@localhost:5435/arb_test?connect_timeout=3")
	if !strings.Contains(got, "connect_timeout=3") {
		t.Errorf("已有 connect_timeout=3 应保留，got=%s", got)
	}
}

// TestNormalizeDSN_ConnectTimeoutParsedByPgx T1 补充锚：pgx 必须真认这个参数
// （ConnectTimeout=5s）——防「追加了个 pgx 不认的键=静默无效」伪绿。
func TestNormalizeDSN_ConnectTimeoutParsedByPgx(t *testing.T) {
	cfg, err := pgx.ParseConfig(normalizeDSN("postgres://arb:arb@localhost:5435/arb_test"))
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %v", err)
	}
	if cfg.ConnectTimeout != 5*time.Second {
		t.Errorf("pgx ConnectTimeout = %v, want 5s（红=connect_timeout 未被 pgx 识别）", cfg.ConnectTimeout)
	}
}

// TestConnectTimeout_FastFail T1 行为锚：不可达主机（blackhole，SYN 无应答）在
// connect_timeout=5 下必须秒级快败（旧径=OS 默认 ~130s 挂死）。
func TestConnectTimeout_FastFail(t *testing.T) {
	dsn := normalizeDSN("postgres://postgres:test@10.255.255.1:5432/arb_test")
	start := time.Now()
	conn, err := pgx.Connect(context.Background(), dsn)
	elapsed := time.Since(start)
	if err == nil {
		conn.Close(context.Background())
		t.Fatal("blackhole 地址不应连接成功")
	}
	if elapsed > 20*time.Second {
		t.Errorf("连接耗时 %v（>20s），connect_timeout=5 快败未生效（旧径 120s 挂死）", elapsed)
	}
}

// TestPerPkgDSN D-351 T1（S3 纯函数锚）+ D-373 泛化改义：db 名以 _test 结尾的共享基名
// 一律改写 <基名>_<tag>（arb_test 既有语义不变 + arb_service_test/arb_exec_test 新收编 +
// 任意 my_test 同权）；已 tag 名（arb_test_scan，_scan 不在结尾）幂等不再追加；
// 显式非 _test 结尾名（arb_test_local）=调用方自担隔离，原样放行；空 tag 原样。
func TestPerPkgDSN(t *testing.T) {
	// 改写集：*_test 结尾基名 → <基名>_<tag>
	for _, tc := range []struct{ base, wantPath string }{
		{"arb_test", "/arb_test_scan"},                 // 既有行为不变（向后兼容锚）
		{"arb_service_test", "/arb_service_test_scan"}, // D-373 新收编（原「CI 显式库名」注释放行的漏网通道）
		{"arb_exec_test", "/arb_exec_test_scan"},       // D-373 新收编
		{"my_test", "/my_test_scan"},                   // 任意 *_test 名同权隔离（命名注入更严不是更松）
	} {
		got, changed := perPkgDSN("postgres://u:p@h:5432/"+tc.base+"?sslmode=disable", "scan")
		if !changed {
			t.Errorf("%s 应改写（D-373 *_test 后缀判定）: got=%s", tc.base, got)
			continue
		}
		if u, _ := url.Parse(got); u.Path != tc.wantPath {
			t.Errorf("%s 改写后 Path = %q, want %q", tc.base, u.Path, tc.wantPath)
		}
	}
	// 不改写集：已 tag 名幂等 + 显式命名库
	for _, dsn := range []string{
		"postgres://u:p@h:5432/arb_test_scan",  // 已 tag：幂等不再追加（_scan 不在结尾）
		"postgres://u:p@h:5432/arb_test_local", // 显式命名库：调用方自担隔离责任
	} {
		if got, changed := perPkgDSN(dsn, "scan"); changed {
			t.Errorf("不改写集命中: %s → %s", dsn, got)
		}
	}
	if got, changed := perPkgDSN("postgres://u:p@h:5432/arb_test", ""); changed {
		t.Errorf("空 tag（推导失败）应原样返回: got=%s", got)
	}
}
