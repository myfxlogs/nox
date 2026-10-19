#!/bin/bash
# state-audit 呆行扫描 cron wrapper（注记60/62 结构性堵口——翻态滞后呆行日拍）。
# 逻辑：/opt/nox-git bare clone fetch 同步 → state-audit 扫 master:STATE.md
# 票行引用核拓扑 → 呆行候选 warn 落 alerts（24h 同键去重）。
set -a
source /opt/nox/.env
set +a
# D-114：DB 端口取 nox-postgres 运行容器发布值（宿主拓扑权威源；勿写死 5433=alphaforge 化石端口）。
NOX_PG_PORT="$(docker port nox-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
if [ -z "$NOX_PG_PORT" ]; then
	echo "$(date -u +%FT%TZ) state-audit-run: nox-postgres 发布端口解析失败" >&2
	exit 1
fi
export DB_DSN="postgres://nox:nox@127.0.0.1:${NOX_PG_PORT}/nox?sslmode=disable"
# 二进制未建（merge 后首轮 build_host_tools 前）静默退——cron 先行不喷日志。
[ -x /opt/nox/bin/state-audit ] || exit 0
# bare clone 未备（初始化径=git clone --mirror git@github.com:myfxlogs/nox.git /opt/nox-git）。
[ -d /opt/nox-git ] || { echo "$(date -u +%FT%TZ) state-audit-run: /opt/nox-git 缺失" >&2; exit 1; }
/opt/nox/bin/state-audit -repo /opt/nox-git -fetch 2>&1
