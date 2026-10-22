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

# D-667 族 陈件 prune：git 已删 .go 永留 SG 进目录编译面炸 build（坑正典
# deploy-sync-stale-files——D-663/D-665 部署两咬实证）。差集=SG find '*.go'
# − git ls-files '*.go'，按清单定点 rm。**禁 rsync --delete**：--files-from
# 下会扫删 .env/logs/web/dist 非跟踪活件（实测证伪）。干跑=差集只报不删。
git -c core.quotepath=false ls-files '*.go' > /tmp/deploy-sync-tracked-go.$$
ssh "$SG" "cd $DEST && find . -name '*.go' -type f" | sed 's|^\./||' | sort > /tmp/deploy-sync-sg-go.$$
sort -u /tmp/deploy-sync-tracked-go.$$ > /tmp/deploy-sync-tracked-go-sort.$$
comm -13 /tmp/deploy-sync-tracked-go-sort.$$ /tmp/deploy-sync-sg-go.$$ > /tmp/deploy-sync-stale-go.$$
stale_n=$(wc -l < /tmp/deploy-sync-stale-go.$$)
if [ "${1:-}" = "-n" ]; then
	[ "$stale_n" -gt 0 ] && echo "[deploy_sync] 干跑差集 $stale_n 件:" && cat /tmp/deploy-sync-stale-go.$$
else
	if [ "$stale_n" -gt 0 ]; then
		# 定点删——清单件全为 .go 且不在跟踪面（.env/logs/bin 等保护面非
		# .go 域，天然不在差集内；空清单零删件）。
		ssh "$SG" "cd $DEST && xargs -d '\n' rm -v" < /tmp/deploy-sync-stale-go.$$ | tail -5
		echo "[deploy_sync] prune 陈件 $stale_n 件"
	fi
fi
rm -f /tmp/deploy-sync-tracked-go.$$ /tmp/deploy-sync-sg-go.$$ \
	/tmp/deploy-sync-tracked-go-sort.$$ /tmp/deploy-sync-stale-go.$$

# D-547 补：宿主侧二进制（universe-radar/xasset-monitor/credread）随源码重建——
# rsync 只推 git 跟踪面，不重建则 cron 跑旧码（10-15 部署漏步实证）。
ssh "$SG" "cd $DEST && bash scripts/build_host_tools.sh" >/dev/null
