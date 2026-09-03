// Package testutil 测试基础设施共享助手。
//
// TestDSN fail-closed 门禁：2026-08-25 生产 facts 表被测试 TRUNCATE 事故根因修复。
// 7 处测试助手原仅判「未设置→Skip」，不判「设置值指向生产」→ 施工方误指生产库时
// 测试直接清空生产表。TestDSN 统一为：未设置→Fatalf（D-207）/ 逃生门→Skip / db 名不含 _test→Fatalf 拒绝。
//
// D-207 加固：DSN 缺失从静默 SKIP 改为 loud-FAIL（「没跑」不得长得像「过了」=假绿之源，
// D-189/D-190 三轮返修实证）；DSN 规范化统一追加 pool_max_conns/pool_min_conns/lock_timeout
// options 参数（62 个 TRUNCATE 位点一处兜底，pgx 认 URL options 参数已实证）。
package testutil

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
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
//   - 无 pool_max_conns → 追加 pool_max_conns=4（统一局部/CI 行为，避开紧池死锁与 fd 压力两极）
//   - 无 pool_min_conns → 追加 pool_min_conns=0
//   - 无 options=      → 追加 options=-c%20lock_timeout=5s（62 个 TRUNCATE 位点一处兜底，
//     pgx 认 URL options 参数已实证：SHOW lock_timeout=3s 实测通过）
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
	return normalizeDSN(dsn)
}

// normalizeDSN 追加缺失的 pool 参数与 lock_timeout options（D-207）。
// DSN 含 `?` 用 `&` 拼接，否则用 `?`；逐项判存，已有则跳过。
func normalizeDSN(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	add := func(param, value string) {
		if strings.Contains(dsn, param+"=") {
			return
		}
		dsn += sep + param + "=" + value
		sep = "&"
	}
	add("pool_max_conns", "4")
	add("pool_min_conns", "0")
	if !strings.Contains(dsn, "options=") {
		dsn += sep + "options=-c%20lock_timeout=5s"
	}
	return dsn
}
