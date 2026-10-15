#!/usr/bin/env bash
# deploy_sync.sh — 本机→SG 源码同步唯一入口（2026-10-09 xasset 断写事故后钉死）。
# 只推 git 跟踪文件：.env/bin/logs/docs报告/运行时产物一律不碰 SG 侧（本机副本陈旧或根本不存在）。
# 用法：./scripts/deploy_sync.sh [-n 干跑]
set -euo pipefail
cd "$(dirname "$0")/.."
SG="${NOX_SG_HOST:-root@192.229.85.35}"
DEST="/opt/nox/"
EXTRA=(); [ "${1:-}" = "-n" ] && EXTRA=(-n)
# --files-from=git 跟踪清单：非跟踪文件不推不删（SG 侧 bin/.env/logs/报告 原样保留）。
# core.quotepath=false：CJK 文件名原样输出（默认 C 引用转义会让 rsync link_stat 失配）。
# shellcheck disable=SC2046
rsync -az "${EXTRA[@]}" --files-from=<(git -c core.quotepath=false ls-files) ./ "$SG:$DEST"
# D-547 补：宿主侧二进制（universe-radar/xasset-monitor/credread）随源码重建——
# rsync 只推 git 跟踪面，不重建则 cron 跑旧码（10-15 部署漏步实证）。
ssh "$SG" "cd $DEST && bash scripts/build_host_tools.sh" >/dev/null
