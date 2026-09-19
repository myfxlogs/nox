// Package testutil 测试基础设施共享助手。
//
// TestDSN fail-closed 门禁：2026-08-25 生产 facts 表被测试 TRUNCATE 事故根因修复。
// 7 处测试助手原仅判「未设置→Skip」，不判「设置值指向生产」→ 施工方误指生产库时
// 测试直接清空生产表。TestDSN 统一为：未设置→Fatalf（D-207）/ 逃生门→Skip / db 名不含 _test→Fatalf 拒绝。
//
// D-207 加固：DSN 缺失从静默 SKIP 改为 loud-FAIL（「没跑」不得长得像「过了」=假绿之源，
// D-189/D-190 三轮返修实证）；DSN 规范化追加 lock_timeout options 参数
// （62 个 TRUNCATE 位点一处兜底，pgx 认 URL options 参数已实证）。
// v1.1 修订：不追加 pool_* 参数（pgx.Connect 单连接路径不认 pgxpool 专有参数）。
// D-351 三杠杆（测试连接稳定性）：S1 SharedStore 包进程级共享池（per-test 池=每包 dial
// 数百次是 D-350 两轮复审假红的连接层根因 amplifier）；S2 normalizeDSN 追加
// connect_timeout=5（连接类故障 120s 挂死→5s 快败）；S3 逐包库名（env 指向共享 arb_test
// 时改写 arb_test_<包tag>+幂等建库——多包/多席并行零互踩，-p 1 从纪律降级为非必要）。
package testutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isTestDB dsn 的 db 名必须含 "_test"（arb_test/arb_exec_test/arb_service_test）；
// 生产 db=arb 不含 → false（fail-closed，防测试 TRUNCATE 生产表）。
func isTestDB(dsn string) bool {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false
	}
	return strings.Contains(cfg.Database, "_test")
}

// TestDSN 测试用 DSN 门禁：envVar 未设置→Fatalf（D-207 loud-FAIL，假绿之源）；
// 显式逃生门 ARB_TEST_ALLOW_SKIP_DSN=1 才 Skipf（退出 DB 测试必须是自觉行为）；
// 指向非测试库→Fatalf 拒绝。envVar 如 "ARB_TEST_PG_DSN" / "ARB_EXEC_TEST_PG_DSN"。
//
// 返回值经 DSN 规范化（追加而非覆盖，已有则跳过）：
//   - 无 options= → 追加 options=-c%20lock_timeout=5s（62 个 TRUNCATE 位点一处兜底，
//     pgx 认 URL options 参数已实证：SHOW lock_timeout=3s 实测通过）
//   - 追加 connect_timeout=5（D-351 S2：连接类故障从 pgx 回退链 120s 挂死变 5s 快败）
//   - db 名恰为共享 arb_test → 改写 arb_test_<调用方包tag> 并幂等建库（D-351 S3：
//     多包/多席并行零互踩；其他库名=CI 四库配方等显式指定，原样不动）
//
// D-207 v1.1 修订：不再追加 pool_max_conns/pool_min_conns——TestDSN 消费方含
// pgx.Connect（单连接）路径（internal/exec/execute_guard_test.go），pool_* 是
// pgxpool 专有参数，pgx.Connect 报 unrecognized configuration parameter。
// 行为中性：pgxpool 默认 defaultMaxConns=4 / min=0（pgx pool.go:19），与原追加值相同。
func TestDSN(t *testing.T, envVar string) string {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		if os.Getenv("ARB_TEST_ALLOW_SKIP_DSN") == "1" {
			t.Skipf("%s 未设置（ARB_TEST_ALLOW_SKIP_DSN=1 逃生门启用，自觉跳过 DB 测试）", envVar)
		}
		t.Fatalf("D-207：%s 未设置——DSN 缺失不再静默 SKIP（假绿之源：D-189/D-190 三轮返修实证「没跑」长得像「过了」）。设 %s=<测试库 DSN> 或显式 ARB_TEST_ALLOW_SKIP_DSN=1 自觉跳过", envVar, envVar)
	}
	if !isTestDB(dsn) {
		t.Fatalf("%s 指向非测试库（db 名不含 _test），测试会 TRUNCATE 生产表，拒绝执行", envVar)
	}
	out := normalizeDSN(dsn)
	if rewritten, changed := perPkgDSN(out, callerPackageTag()); changed {
		if err := ensureDB(rewritten); err != nil {
			t.Fatalf("D-351 S3 逐包建库失败（fail-closed 不吞）：%v", err)
		}
		out = rewritten
	}
	return out
}

// normalizeDSN 追加缺失的连接层参数（D-207 v1.1：不追加 pool 参数）。
//   - lock_timeout：无 options= 时追加 options=-c%20lock_timeout=5s（TRUNCATE 一处兜底）；
//     已有 options= 则跳过（不能二次追加，pgx 拒重名 query 参数语义）。
//   - connect_timeout=5（D-351 S2）：无该参数时追加——连接类故障（dial 超时/回退链）
//     从 OS 默认 ~130s 挂死变 5s 快败+清晰报错；已有自定义值原样保留不覆盖。
//   - pool_max_conns/pool_min_conns 不追加——TestDSN 消费方含 pgx.Connect 单连接路径，
//     pool_* 是 pgxpool 专有参数。pgxpool 默认 defaultMaxConns=4/min=0 与原追加值相同（行为中性）。
func normalizeDSN(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if !strings.Contains(dsn, "options=") {
		dsn += sep + "options=-c%20lock_timeout=5s"
		sep = "&"
	}
	if !strings.Contains(dsn, "connect_timeout=") {
		dsn += sep + "connect_timeout=5"
	}
	return dsn
}

// perPkgDSN D-351 S3：db 名恰为共享 arb_test → 改写 arb_test_<tag>（逐包隔离，
// 并行多包/多席共用实例零互踩）；其他库名（CI 四库配方/arb_test_local 等显式指定）
// 与空 tag（调用方推导失败）原样不动。
func perPkgDSN(dsn, tag string) (string, bool) {
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/arb_test" || tag == "" {
		return dsn, false
	}
	u.Path = "/arb_test_" + tag
	return u.String(), true
}

// callerPackageTag 从调用方测试文件路径派生包 tag（如 scan/pgstore/dashboard/core）——
// 跳过 testutil 自身非测试帧（TestDSN/SharedStore 等包装链）后取首个外部帧。
func callerPackageTag() string {
	for skip := 1; skip < 16; skip++ {
		pc, file, _, ok := runtime.Caller(skip)
		if !ok {
			return ""
		}
		if fn := runtime.FuncForPC(pc); fn != nil &&
			strings.HasPrefix(fn.Name(), "arb/internal/testutil") &&
			!strings.HasSuffix(file, "_test.go") {
			continue // testutil/testutil-storetest 内部包装链帧（非测试源）
		}
		return sanitizeTag(filepath.Base(filepath.Dir(file)))
	}
	return ""
}

// sanitizeTag 包目录名 → 合法 DB 名后缀段（防怪目录名注入库名）。
func sanitizeTag(tag string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, tag)
}

// ensuredDBs 进程级已建库缓存（避免同库重复 CREATE DATABASE 往返）。
var ensuredDBs sync.Map

// ensureDB D-351 S3 幂等建库：连同名实例的 postgres 库 CREATE DATABASE；
// 已存在（SQLSTATE 42P04 duplicate_database，两进程并发首建竞态容忍）跳过；
// 其他错误原样上抛（F6 建库失败不吞）。成功后入进程缓存。
func ensureDB(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("ensureDB 解析 DSN: %w", err)
	}
	dbname := strings.TrimPrefix(u.Path, "/")
	if dbname == "" {
		return fmt.Errorf("ensureDB：DSN 无库名")
	}
	if _, ok := ensuredDBs.Load(dbname); ok {
		return nil
	}
	admin := *u
	admin.Path = "/postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		return fmt.Errorf("ensureDB 连 postgres 库: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbname}.Sanitize()); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P04" {
			return fmt.Errorf("ensureDB CREATE DATABASE %s: %w", dbname, err)
		}
	}
	ensuredDBs.Store(dbname, true)
	return nil
}
