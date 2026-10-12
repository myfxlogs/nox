#!/usr/bin/env bash
# paper_window_report.sh —— D-023 ③：paper long_cfd_short_perp 逐日净额报告（观测窗固化查询）。
# 观测窗起点=app_config NOX_PAPER_WINDOW_START（D-033 R1③ 单源；CLI 参数可覆盖——原
# 硬编码 2026-10-02 23:06 UTC 已退役，窗起点运维经 app_config 热改与进程内日报同源）。
# cohort=窗内新开行 ∪ 窗始已 active 行。
# 口径注记（诚实边界）：paper_spot_carry.accrued_usd 为**累计值**，库内无逐日 accrual 流水——
# 「逐日净额」靠逐日运行本脚本对照累计值推演（每次运行输出当日快照+相对上次运行增量，
# 状态存 docs/报告/paper_window_state/）；exit 行以 exit_ts 当日 realized（=退出时 accrued）计。
# 用法：scripts/paper_window_report.sh [--since '2026-10-02 23:06+00']
set -euo pipefail

PSQL="docker exec nox-postgres psql -U nox -d nox -v ON_ERROR_STOP=1 -t -A"
SINCE="${1:-$($PSQL -c "SELECT value FROM app_config WHERE key='NOX_PAPER_WINDOW_START'" | tr -d '[:space:]')}"
if [[ -z "$SINCE" ]]; then
  echo "FAIL: app_config NOX_PAPER_WINDOW_START 未配置且未传 --since" >&2
  exit 1
fi

echo "== paper long_cfd_short_perp 逐日净额报告（窗口起点 $SINCE；生成 $(date -u +%FT%TZ)）=="

echo "-- 1. cohort 总览（窗内新开 ∪ 窗始 active）--"
$PSQL -c "
SELECT status, count(*), COALESCE(sum(accrued_usd), 0) AS accrued_sum, COALESCE(sum(qty_usd), 0) AS notional_sum
FROM paper_spot_carry
WHERE structure = 'long_cfd_short_perp'
  AND (entry_ts >= '$SINCE' OR (status = 'active' AND entry_ts < '$SINCE'))
GROUP BY status ORDER BY status;"

echo "-- 2. 在仓行逐日累计（按 last_accrue_ts 日分桶=累积推进日；非逐日历史，逐日增量靠逐日运行对照）--"
$PSQL -c "
SELECT COALESCE(to_char(last_accrue_ts, 'MM-DD'), 'never') AS accrue_day,
       count(*) AS rows, COALESCE(round(sum(accrued_usd)::numeric, 4), 0) AS accrued_sum
FROM paper_spot_carry
WHERE structure = 'long_cfd_short_perp'
  AND (entry_ts >= '$SINCE' OR (status = 'active' AND entry_ts < '$SINCE'))
  AND status = 'active'
GROUP BY 1 ORDER BY 1;"

echo "-- 3. 窗内退出行（exit_ts 日 = realized 日；realized=退出时 accrued_usd）--"
$PSQL -c "
SELECT to_char(exit_ts, 'MM-DD') AS exit_day, count(*) AS rows,
       COALESCE(round(sum(accrued_usd)::numeric, 4), 0) AS realized_sum,
       string_agg(DISTINCT exit_reason, ',') AS reasons
FROM paper_spot_carry
WHERE structure = 'long_cfd_short_perp'
  AND status = 'exited' AND exit_ts >= '$SINCE'
GROUP BY 1 ORDER BY 1;"

echo "-- 4. 窗口净额合计 = 在仓累计 + 退出 realized --"
$PSQL -c "
SELECT (SELECT COALESCE(sum(accrued_usd), 0) FROM paper_spot_carry
        WHERE structure = 'long_cfd_short_perp'
          AND (entry_ts >= '$SINCE' OR (status = 'active' AND entry_ts < '$SINCE')) AND status = 'active')
     + (SELECT COALESCE(sum(accrued_usd), 0) FROM paper_spot_carry
        WHERE structure = 'long_cfd_short_perp' AND status = 'exited' AND exit_ts >= '$SINCE')
       AS window_net_usd;"
