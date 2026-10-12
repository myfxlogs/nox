#!/bin/bash
# D-307 CEX 全量合约雷达（crontab wrapper）
# 每小时 17 分调度：三所全量 perp 枚举+diff+funding 雷达，只发现只报告
# D-538 扩围：端点取 .env/appConfig 权威值，禁脚本覆盖（化石端点同型教训）。
set -a
source /opt/arb/.env
set +a
# D-114：DB 端口取 arb-postgres 运行容器发布值（同 xasset-monitor-run.sh 口径）。
ARB_PG_PORT="$(docker port arb-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
if [ -z "$ARB_PG_PORT" ]; then
	echo "$(date -u +%FT%TZ) universe-radar-run: arb-postgres 发布端口解析失败" >&2
	exit 1
fi
export DB_DSN="postgres://arb:arb@127.0.0.1:${ARB_PG_PORT}/arb?sslmode=disable"
/opt/arb/bin/universe-radar 2>&1
