#!/usr/bin/env bash
# restore_test.sh D-159 W6a S3：备份恢复演练（恢复到临时库 arb_restore_test，校验后清理）。
# 不碰生产库。输出 "RESTORE_TEST_OK" 或错误。
#
# 用法：scripts/restore_test.sh [DUMP_FILE] [PG_ADMIN_DSN]
# DUMP_FILE 默认取 /opt/nox-backups/ 最新 nox_*.sql.gz
# PG_ADMIN_DSN 默认 postgres://nox:nox@localhost:5434/postgres?sslmode=disable（有 CREATE DATABASE 权限）

set -euo pipefail

BACKUP_DIR="/opt/nox-backups"
# D-016 席位后缀：与 pgstore backup_integrity_test 的 restoreTestDBName 同派生规则
# （NOX_TEST_SEAT 优先，缺省=仓根目录名小写非字母数字转下划线）——多席并发不互踩。
_restore_seat="${NOX_TEST_SEAT:-$(basename "$(git rev-parse --show-toplevel 2>/dev/null || pwd)")}"
_restore_seat=$(echo "$_restore_seat" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_]/_/g')
TEST_DB="arb_restore_test_${_restore_seat}"
PG_ADMIN_DSN="${2:-postgres://nox:nox@127.0.0.1:5434/postgres?sslmode=disable}" # D-021：恢复演练/临时库在测试实例

# 取最新 dump
if [ -n "${1:-}" ]; then
	DUMP_FILE="$1"
else
	DUMP_FILE=$(ls -t "$BACKUP_DIR"/nox_*.sql.gz 2>/dev/null | head -1 || true)
fi

if [ -z "$DUMP_FILE" ] || [ ! -f "$DUMP_FILE" ]; then
	echo "RESTORE_TEST_FAIL: no dump file found in $BACKUP_DIR" >&2
	exit 1
fi

echo "[$(date -u +%FT%TZ)] restore test start: dump=$DUMP_FILE test_db=$TEST_DB"

# 校验 checksum（若 .sha256 存在）
SHA_FILE="${DUMP_FILE}.sha256"
if [ -f "$SHA_FILE" ]; then
	if ! (cd "$BACKUP_DIR" && sha256sum -c "$(basename "$SHA_FILE")" >/dev/null 2>&1); then
		echo "RESTORE_TEST_FAIL: checksum mismatch for $DUMP_FILE" >&2
		exit 1
	fi
	echo "[$(date -u +%FT%TZ)] checksum OK"
fi

# 从 PG_ADMIN_DSN 推导测试库 DSN（替换 dbname）
TEST_DSN=$(echo "$PG_ADMIN_DSN" | sed "s|/[^/]*?|/$TEST_DB?|")

# 清理旧测试库 + 建新测试库
psql "$PG_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $TEST_DB;" >/dev/null 2>&1 || true
if ! psql "$PG_ADMIN_DSN" -c "CREATE DATABASE $TEST_DB OWNER arb;" >/dev/null; then
	echo "RESTORE_TEST_FAIL: CREATE DATABASE $TEST_DB error" >&2
	exit 1
fi

# 恢复（错误捕获）
RESTORE_ERR=$(gunzip -c "$DUMP_FILE" | psql "$TEST_DSN" -q 2>&1 >/dev/null) || true
if [ -n "$RESTORE_ERR" ]; then
	echo "RESTORE_TEST_FAIL: psql restore error: $RESTORE_ERR" >&2
	psql "$PG_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $TEST_DB;" >/dev/null 2>&1 || true
	exit 1
fi

# 校验：表数量 + schema_migrations 版本 + 行数抽样
TABLE_COUNT=$(psql "$TEST_DSN" -t -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';" | tr -d ' ')
MIGRATION_VER=$(psql "$TEST_DSN" -t -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1;" | tr -d ' ')
POSITIONS_COUNT=$(psql "$TEST_DSN" -t -c "SELECT count(*) FROM positions;" 2>/dev/null | tr -d ' ' || echo "0")

echo "[$(date -u +%FT%TZ)] tables=$TABLE_COUNT migrations=$MIGRATION_VER positions=$POSITIONS_COUNT"

if [ -z "$TABLE_COUNT" ] || [ "$TABLE_COUNT" -lt 10 ]; then
	echo "RESTORE_TEST_FAIL: table count too low ($TABLE_COUNT)" >&2
	psql "$PG_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $TEST_DB;" >/dev/null 2>&1 || true
	exit 1
fi

if [ -z "$MIGRATION_VER" ]; then
	echo "RESTORE_TEST_FAIL: no schema_migrations version" >&2
	psql "$PG_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $TEST_DB;" >/dev/null 2>&1 || true
	exit 1
fi

# 清理
psql "$PG_ADMIN_DSN" -c "DROP DATABASE IF EXISTS $TEST_DB;" >/dev/null

echo "RESTORE_TEST_OK"
