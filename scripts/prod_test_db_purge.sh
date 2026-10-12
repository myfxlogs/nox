#!/usr/bin/env bash
# prod_test_db_purge.sh —— D-021：生产实例测试库清退——drop 全部
# arb_test*/arb_ci_test*/arb_d0*_test*/arb_restore_test* 库（测试面已整体迁独立实例 5434）。
# 默认 dry-run 列清单；--apply 真删。防呆：只对生产实例操作（位置参 1 显式传 DSN 才可指他处）；
# 有活动连接的库跳过（正被测试用的会话不可劫持——理论上迁移后生产实例不该再有测试连接）。
# 用法：scripts/prod_test_db_purge.sh [--apply]
set -euo pipefail

APPLY=0
if [ "${1:-}" = "--apply" ]; then APPLY=1; shift; fi
# D-114：缺省管理端端口取 arb-postgres 运行容器发布值（SG=15433，5433=alphaforge）。
if [ $# -ge 1 ]; then
	PG_ADMIN_DSN="$1"
else
	ARB_PG_PORT="$(docker port arb-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
	[ -n "$ARB_PG_PORT" ] || { echo "PURGE_FAIL: arb-postgres 端口解析失败（位置参传 DSN 可旁路）" >&2; exit 1; }
	PG_ADMIN_DSN="postgres://arb:arb@127.0.0.1:${ARB_PG_PORT}/postgres?sslmode=disable"
fi

mapfile -t CANDIDATES < <(psql "$PG_ADMIN_DSN" -t -A -c \
	"SELECT datname FROM pg_database WHERE datname ~ '^(arb_test|arb_ci_test|arb_d[0-9]+_test|arb_restore_test)'" | sort)
mapfile -t BUSY < <(psql "$PG_ADMIN_DSN" -t -A -c \
	"SELECT DISTINCT datname FROM pg_stat_activity WHERE datname ~ '^arb_' AND state <> 'idle'")

dropped=0
for db in ${CANDIDATES[@]+"${CANDIDATES[@]}"}; do
	busy=0
	for b in ${BUSY[@]+"${BUSY[@]}"}; do
		[ "$b" = "$db" ] && busy=1 && break
	done
	if [ $busy -eq 1 ]; then
		echo "SKIP(活动连接): $db"
		continue
	fi
	if [ $APPLY -eq 1 ]; then
		if psql "$PG_ADMIN_DSN" -q -c "DROP DATABASE IF EXISTS \"$db\""; then
			echo "DROPPED: $db"
			dropped=$((dropped + 1))
		else
			echo "DROP_FAIL: $db" >&2
		fi
	else
		echo "WOULD_DROP: $db"
	fi
done
echo "PURGE_DONE applied=$dropped candidates=${#CANDIDATES[@]}（默认 dry-run，--apply 真删；目标=生产实例零测试库）"
