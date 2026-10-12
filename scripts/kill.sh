#!/usr/bin/env bash
# kill.sh — 自动交易急停脚本（D-077 修复⑩ G6）
#
# 将 autotrade_state 置为 CLOSE_ONLY：gate 全拒新开仓，仅允许平仓。
# 幂等可重复跑。恢复见脚本输出提示。
#
# 用法：bash scripts/kill.sh [reason]
#   reason  可选，默认 "manual kill <timestamp>"

set -euo pipefail

REASON="${1:-manual kill $(date -u +%Y-%m-%dT%H:%M:%SZ)}"

echo "[kill] 设置 autotrade_state → CLOSE_ONLY"
echo "[kill] reason: ${REASON}"
echo ""

docker compose -f /opt/nox/docker-compose.yml exec -T nox-postgres \
  psql -U nox -d nox -v ON_ERROR_STOP=1 -v reason="$REASON" <<SQL
UPDATE autotrade_state SET state='CLOSE_ONLY', reason=:'reason', updated_at=now();
SELECT state, reason, updated_at FROM autotrade_state;
SQL

echo ""
echo "[kill] 已急停。gate 将拒绝所有新开仓（仅允许平仓）。"
echo "[kill] 恢复命令："
echo "  docker compose -f /opt/nox/docker-compose.yml exec -T nox-postgres \\"
echo "    psql -U nox -d nox -c \"UPDATE autotrade_state SET state='RUNNING', reason='manual recover', updated_at=now();\""
