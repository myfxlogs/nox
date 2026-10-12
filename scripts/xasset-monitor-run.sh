#!/bin/bash
# D-299 PAXG-XAU 跨市场套利监控（crontab wrapper），5min 间隔。
# D-538：BINANCE_PROXY_URL/CA 硬编码清除——端点取 .env/appConfig 权威值，
# 禁在脚本内覆盖（直连 IP:8443 已被 ant iptables DROP，化石端点教训）。
set -a
source /opt/nox/.env
set +a
# D-114：DB 端口取 nox-postgres 运行容器发布值（宿主拓扑权威源；SG=15433，
# 5433=alphaforge 化石端口——迁移期临时转发断流后写死端口裸奔至异构实例）。
NOX_PG_PORT="$(docker port nox-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
if [ -z "$NOX_PG_PORT" ]; then
	echo "$(date -u +%FT%TZ) xasset-monitor-run: nox-postgres 发布端口解析失败" >&2
	exit 1
fi
export DB_DSN="postgres://nox:nox@127.0.0.1:${NOX_PG_PORT}/nox?sslmode=disable"
# 9-16 决策席裁定：飞书告警屏蔽（恒真噪音——进场价差不足/进场无利每 5min 恒触发，
# D-300 裁 13 去重另单未实施前先静音；采集入库不受影响）。恢复=删此行。
export NOX_FEISHU_APP_ID=""
/opt/nox/bin/xasset-monitor 2>&1
