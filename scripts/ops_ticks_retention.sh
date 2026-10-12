#!/bin/sh
# ops_ticks_retention.sh D-213 S1：ticks 表 72h 保留（每日 cron）。
# 唯一读侧=internal/store/pgstore/brokers_fx.go USDPer 七直盘 24h 窗，72h=3 倍余量。
# DELETE 72h 外 + VACUUM ANALYZE ticks（防死元组堆积表膨胀；autovacuum 对大表触发滞后）。幂等。
#
# D-114：连接走容器内 psql——宿主端口随拓扑漂移（SG=15433，5433=alphaforge），
# docker exec 与端口解耦。容器内 arb 超级用户有 DELETE 权限。
# ⚠️ 勿读 .env 的 ARB_AI_PG_DSN——那是 arb_ro 只读 AI DSN（grants 仅 SELECT，无 DELETE 权限，已核），
#    照抄=每天权限拒绝+cron 静默假绿（运维版 D-207 假绿教训）。
#
# crontab：17 4 * * * /opt/arb/scripts/ops_ticks_retention.sh >> /opt/arb-backups/ticks_retention.log 2>&1
# 失败 exit 1（可观测，不吞错）。稳态 ~450MB 封顶（DELETE 空间表内复用不还 OS，故每日 VACUUM 必要）。
set -u

PSQL="docker exec arb-postgres psql -U arb -d arb"

# DELETE 72h 外（RETURNING 计数；幂等：无符合行=0）
N=$($PSQL -t -A -c "WITH del AS (DELETE FROM ticks WHERE ts < now() - interval '72 hours' RETURNING 1) SELECT count(*) FROM del") || {
  echo "[$(date -u +%FT%TZ)] ticks retention FAIL: DELETE error" >&2
  exit 1
}

# VACUUM ANALYZE（不能在事务块内，单独调用；防死元组堆积致表膨胀）
$PSQL -c "VACUUM ANALYZE ticks" >/dev/null || {
  echo "[$(date -u +%FT%TZ)] ticks retention FAIL: VACUUM ANALYZE error" >&2
  exit 1
}

echo "[$(date -u +%FT%TZ)] ticks retention OK: deleted $N rows"
