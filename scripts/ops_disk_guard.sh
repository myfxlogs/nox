#!/bin/sh
# ops_disk_guard.sh 磁盘守卫（D-213 v2，2026-09-04 修订；D-277 B4 GOCACHE 解耦）。
# 逻辑：①主动项每次运行都执行（docker builder prune --filter until=24h，cache 超 24h 即清，
#   重构建日天然封顶，不当主力只兜底）②根盘 >75% → 自愈清理（docker builder
#   cache / dangling 镜像 / journal / tmp 旧压缩包 + go-build 残留目录）→ 仍 >75% 则告警入 alerts
#   表（dashboard 铃铛可见，level=critical）。清理本身成功降下来的只发 warn 级留痕。
# D-277 B4：go build cache 清理从 >75% 改为 >85% 才清（GOCACHE=/tmp/gocache-shared 解耦，
#   避免 75% 触发即清致构建瞬态红；85% 仍保留兜底防爆盘）。
# 历史：2026-08-22 建（阈值 85）→ 2026-08-25 修订（第二次事故 go-build 7.7G 顶爆盘→postgres
#   PANIC→公网 502，阈值 85→80 + 自愈清单加 go clean -cache/dangling 镜像）→ 2026-09-04 D-213 v2
#   （第三次事故 100%→checkpointer crash→WAL 恢复死锁：阈值 80→75 + 主动 builder prune 治本 +
#   go-build 残留目录清单）。模式结论：守卫不当主力，每个增长源须有硬顶（D-213 S1/S2 治本）。
# cron：13,43 * * * *（每半小时，避开整点）。幂等，无输出=一切正常。
set -u
THRESH=75
# D-114：告警写库走容器内 psql——宿主端口会随部署拓扑变（SG=15433，5433=alphaforge），
# docker exec 与端口解耦；docker daemon 本就须存活（下方清理全是 docker 命令）。
PSQL="docker exec arb-postgres psql -U arb -d arb"

pct() { df -P / | awk 'NR==2{gsub("%","");print $5}'; }

# 主动项（每次运行都执行，不等阈值）：docker builder cache 超 24h 即清，重构建日天然封顶。
docker builder prune --filter until=24h -f >/dev/null 2>&1

P1=$(pct)
[ "$P1" -lt "$THRESH" ] && exit 0

# 自愈清理（均为安全项：可再生产缓存 / journal 一次性 vacuum / tmp 旧压缩包+go-build 残留目录）
# 2026-08-25 增：go-build 缓存（今日事故元凶 7.7G）+ dangling 镜像。
# 2026-09-04 D-213 增：/tmp/go-build* 残留目录（被杀 go 运行的工作目录，9-3 事故 446M）。
# D-277 B4：go build cache 清理改为 >85% 才清（与 GOCACHE=/tmp/gocache-shared 解耦，避免 75% 触发即清致构建瞬态红）。
if [ "$P1" -ge 85 ]; then
	[ -x /usr/local/go/bin/go ] && /usr/local/go/bin/go clean -cache >/dev/null 2>&1
fi
docker builder prune -af >/dev/null 2>&1
docker image prune -f >/dev/null 2>&1
journalctl --vacuum-size=200M >/dev/null 2>&1
find /tmp -maxdepth 1 -type f \( -name '*.tgz' -o -name '*.tar.gz' \) -mtime +1 -delete 2>/dev/null
find /tmp -maxdepth 1 -type d -name 'go-build*' -mtime +1 -exec rm -rf {} + 2>/dev/null

P2=$(pct)
if [ "$P2" -ge "$THRESH" ]; then
  $PSQL -c "INSERT INTO alerts (level, message) VALUES ('critical', '磁盘使用率 ${P1}%→清理后 ${P2}% 仍超 ${THRESH}%：根盘空间紧张，需人工排查大文件（du -sh /root/.cache /var/lib/docker）')" >/dev/null 2>&1
else
  $PSQL -c "INSERT INTO alerts (level, message) VALUES ('warn', '磁盘使用率 ${P1}% 超 ${THRESH}%，自愈清理后 ${P2}%（go-build cache+docker builder cache+dangling 镜像+journal+go-build 残留目录）')" >/dev/null 2>&1
fi
