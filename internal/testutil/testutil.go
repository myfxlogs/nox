// Package testutil 测试基础设施共享助手。
//
// TestDSN fail-closed 门禁：2026-08-25 生产 facts 表被测试 TRUNCATE 事故根因修复。
// 7 处测试助手原仅判「未设置→Skip」，不判「设置值指向生产」→ 施工方误指生产库时
// 测试直接清空生产表。TestDSN 统一为：未设置→Skip / db 名不含 _test→Fatalf 拒绝。
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

// TestDSN 测试用 DSN 门禁：envVar 未设置→Skip；指向非测试库→Fatalf 拒绝。
// envVar 如 "ARB_TEST_PG_DSN" / "ARB_EXEC_TEST_PG_DSN"。
func TestDSN(t *testing.T, envVar string) string {
	t.Helper()
	dsn := os.Getenv(envVar)
	if dsn == "" {
		t.Skipf("%s 未设置，跳过需真库的测试", envVar)
	}
	if !isTestDB(dsn) {
		t.Fatalf("%s 指向非测试库（db 名不含 _test），测试会 TRUNCATE 生产表，拒绝执行", envVar)
	}
	return dsn
}
