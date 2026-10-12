// dsn_guard.go —— 测试 DSN 门禁谓词：isTestDB 库名闸 + isProdInstance 实例端口闸。
// 自 testutil.go 迁出（D-091 行数纪律，纯移动零语义变更）。
package testutil

import (
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// isTestDB dsn 的 db 名必须含 "_test"（nox_test/nox_exec_test/nox_service_test）；
// 生产 db=arb 不含 → false（fail-closed，防测试 TRUNCATE 生产表）。
func isTestDB(dsn string) bool {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false
	}
	return strings.Contains(cfg.Database, "_test")
}

// prodInstancePort D-091 ②：生产实例端口判定源——DB_DSN env 优先（compose 注入的
// 生产面 DSN），未设回落 5433（与 cmd/core/main.go 的 DB_DSN 缺省、compose 5433:5432
// 映射同源，非新发明值）。
func prodInstancePort() uint16 {
	if dsn := os.Getenv("DB_DSN"); dsn != "" {
		if cfg, err := pgx.ParseConfig(dsn); err == nil {
			return cfg.Port
		}
	}
	return 5433
}

// isProdInstance DSN 命中生产实例端口（D-091 ② 第二道硬闸）：isTestDB 只判库名，
// 「显式指 5433 的 *_test 命名库」可绕过（5433 复燃 22 库实证）；端口命中即拦
// （host 不参与——任何 host 的生产端口都非测试实例域）。pgx 无显式端口缺省 5432，
// 不与 5433 撞，缺省 5434 测试实例不受影响。
func isProdInstance(dsn string) bool {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false
	}
	return cfg.Port == prodInstancePort()
}
