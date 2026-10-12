#!/usr/bin/env bash
# test_db_gc.sh —— D-016 R1 + D-062 正则补全：测试库垃圾回收（drop 席位段不对应现存
# git worktree 的测试语义库）。testutil.TestDSN 每席位每包一库（<基名>_<包tag>_<席位段>），
# worktree 删除后其席位库成孤儿；restore/migrate/ci 等固定名辅助库同。
# D-062：候选从 '^arb_test' 收窄漏网改为「含 test 语义的命名模式」白名单
# （'(^|_)test(_|$)' 边界匹配）——覆盖全部实拍历史形态：arb_test_*（席位库）、
# arb_{service,exec,reconcile}_test_*（D-351 共享基名逐包派生）、arb_ci_test_*、
# arb_d[0-9]*_test_*（旧席位命名）、arb_restore_test_*（backup_integrity/restore_test）、
# arb_migrate_*_test_*（pgstore 迁移辅助库）、test_*（test_rebuild_null_* 等裸 test 前缀），
# 并防御未来同约定新形态；边界要求使 latest/contest/testimony 类不含 _test 语义的命名不误中。
# 排除清单显式写死（NEVER_DROP）：四个共享基名 arb_test/arb_service_test/arb_exec_test/
# arb_reconcile_test（TestDSN 缺省落点+改写源，D-021 口径缺省 arb_test 永不可删）+
# 系统库 postgres/template0/template1；保留集=现存 worktree 仓根目录名（含主树，
# 与 testutil.SeatTag 同派生同消毒规则）+ ARB_TEST_SEAT——凡以 _<保留席> 结尾的库一律保留；
# 有活动连接的库一律跳过（正在跑测试的席位不可劫持）。
# 默认 dry-run 只列清单；--apply 才真删（cron 每日一次，D-016 遗留收口）。
# 用法：scripts/test_db_gc.sh [--apply] [PG_ADMIN_DSN]
set -euo pipefail

APPLY=0
if [ "${1:-}" = "--apply" ]; then APPLY=1; shift; fi
PG_ADMIN_DSN="${1:-postgres://arb:arb@127.0.0.1:5434/postgres?sslmode=disable}" # D-021：测试实例 5434

REPO_ROOT="$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel 2>/dev/null || true)"
if [ -z "$REPO_ROOT" ]; then
	echo "GC_ABORT: 不在 git 仓内（保留集无从推导，fail-closed 不删）" >&2
	exit 1
fi

sanitize() { echo "$1" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_]/_/g'; }

# 保留席集合：worktree list 已含主树；显式 ARB_TEST_SEAT 追加。
KEEP_SEATS=""
while read -r wt; do
	[ -n "$wt" ] && KEEP_SEATS="$KEEP_SEATS $(sanitize "$(basename "$wt")")"
done < <(git -C "$REPO_ROOT" worktree list --porcelain | sed -n 's/^worktree //p')
[ -n "${ARB_TEST_SEAT:-}" ] && KEEP_SEATS="$KEEP_SEATS $(sanitize "$ARB_TEST_SEAT")"
if [ -z "${KEEP_SEATS// /}" ]; then
	echo "GC_ABORT: 保留席集合为空（fail-closed 不删）" >&2
	exit 1
fi
echo "保留席位:$(echo "$KEEP_SEATS" | tr ' ' '\n' | sort -u | sed '/^$/d;s/^/ /' | tr -d '\n')"

# 排除清单（显式写死，永不入删集）：四个 TestDSN 共享基名 + PG 系统库。
NEVER_DROP=" arb_test arb_service_test arb_exec_test arb_reconcile_test postgres template0 template1 "

TEST_NAME_RE='(^|_)test(_|$)'

# 候选：含 test 语义命名且不在排除清单且不以 _<保留席> 结尾
# （tails 与席位段一对一启发；同前缀包名误留可接受，误删由忙库豁免+dry-run 兜底）
mapfile -t CANDIDATES < <(psql "$PG_ADMIN_DSN" -t -A -c "SELECT datname FROM pg_database WHERE datname ~ '$TEST_NAME_RE'" | while read -r db; do
	[ -n "$db" ] || continue
	case "$NEVER_DROP" in *" $db "*) continue ;; esac
	keep=0
	for s in $KEEP_SEATS; do
		case "$db" in *_"$s") keep=1; break ;; esac
	done
	[ $keep -eq 0 ] && echo "$db"
done)
mapfile -t BUSY < <(psql "$PG_ADMIN_DSN" -t -A -c "SELECT DISTINCT datname FROM pg_stat_activity WHERE datname ~ '$TEST_NAME_RE'")

dropped=0
for db in ${CANDIDATES[@]+"${CANDIDATES[@]}"}; do
	busy=0
	for b in ${BUSY[@]+"${BUSY[@]}"}; do
		if [ "$b" = "$db" ]; then busy=1; break; fi
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

# D-091 ③：生产实例扫描分支——只报不删（D-021 生产实例零测试库；生产实例任何 drop
# 须人工，本脚本永不触生产实例删除面）。PROD_ADMIN_DSN 可覆盖；D-114：缺省端口取
# arb-postgres 运行容器发布值（SG=15433，5433=alphaforge 化石端口）。
ARB_PG_PORT="$(docker port arb-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
PROD_ADMIN_DSN="${PROD_ADMIN_DSN:-postgres://arb:arb@127.0.0.1:${ARB_PG_PORT}/postgres?sslmode=disable}"
PROD_TEST_DBS="$(psql "$PROD_ADMIN_DSN" -t -A -c "SELECT datname FROM pg_database WHERE datname ~ '$TEST_NAME_RE'" 2>/dev/null || true)"
if [ -n "$PROD_TEST_DBS" ]; then
	echo "PROD_INSTANCE_WARNING: 生产实例发现 test 语义库（须人工核实后 drop，本脚本不删）:"
	echo "$PROD_TEST_DBS" | sed 's/^/  PROD_DB: /'
fi

echo "GC_DONE applied=$dropped candidates=${#CANDIDATES[@]} kept_seats=$(echo $KEEP_SEATS | wc -w)（默认 dry-run，--apply 真删）"
