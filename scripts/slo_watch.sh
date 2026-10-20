#!/usr/bin/env bash
# slo_watch.sh —— 观测 SLO 四告警（宿主 cron 件：RPC p95/pg CPU/慢查询/cron+备份）。
# 每 5min 一拍：三源读→持续窗达标→alerts 落 critical（24h 同文案 dedup——
# 告警不携业务数据仅现象句〔D-004 看门狗语义〕）。
# 用法：*/5 * * * * /opt/nox/scripts/slo_watch.sh >> /var/log/nox-slo.log 2>&1
set -u
DB_CONTAINER="${NOX_PG_CONTAINER:-nox-postgres}"
PG_USER="${NOX_PG_USER:-nox}"
PG_DB="${NOX_PG_DB:-nox}"
STATE_DIR="${NOX_SLO_STATE_DIR:-/tmp/nox-slo}"
mkdir -p "$STATE_DIR"

# emit 走 alert-emit 摄入件（pgstore.InsertAlertDedup 端点——裸 SQL 径已废：dedup 语义+键域一致性经摄入层单源）。
ALERT_EMIT="${NOX_ALERT_EMIT:-/opt/nox/bin/alert-emit}"
emit() { # level message —— InsertAlertDedup 24h 窗（首发保留窗内静默）
  "$ALERT_EMIT" -level "$1" -msg "$2" -dedup 24h >/dev/null 2>&1 || echo "alert-emit 败: $2" >&2
}
streak() { # name → 当前连续超窗数
  local f="$STATE_DIR/$1"
  [[ -f "$f" ]] && cat "$f" || echo 0
}
bump() { echo "$2" > "$STATE_DIR/$1"; }
reset() { rm -f "$STATE_DIR/$1"; }

# ① pg CPU>150% 持续（docker stats 单拍——容器内 metric 不可见，宿主 cron 域）
CPU=$(docker stats "$DB_CONTAINER" --no-stream --format '{{.CPUPerc}}' 2>/dev/null | tr -d '%')
if [[ -n "$CPU" ]] && awk "BEGIN{exit !($CPU>150)}" <<<"$CPU"; then
  n=$(($(streak pg_cpu) + 1)); bump pg_cpu "$n"
  [[ "$n" -ge 2 ]] && emit critical "[SLO] pg CPU 持续 >150%（docker stats 宿主径——${CPU}%）"
else
  reset pg_cpu
fi

# ② 单查询 >5s（pg_stat_statements 均值/max——慢查询面）
SLOW=$(docker exec "$DB_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tA -c "
  SELECT count(*) FROM pg_stat_activity
   WHERE state='active' AND now()-query_start > interval '5 seconds'
     AND query NOT LIKE '%pg_stat_activity%'" 2>/dev/null)
if [[ -n "$SLOW" ]] && [[ "$SLOW" -gt 0 ]]; then
  emit warn "[SLO] 活查询 >5s 在跑（pg_stat_activity 实测 ${SLOW} 条）"
fi

# ③ RPC p95>10s 持续 5min（Connect 面——pprof/mux 侧无内建直读；以 /healthz RTT 代理
# 样本持久高+查询慢并发时收为 proxy——实测锚=2 条连续）
RPC_MS=$(curl -s -o /dev/null -w '%{time_total}' --max-time 15 http://localhost:50051/health 2>/dev/null | awk '{print int($1*1000)}')
if [[ -n "$RPC_MS" ]] && [[ "$RPC_MS" -gt 10000 ]]; then
  n=$(($(streak rpc_p95) + 1)); bump rpc_p95 "$n"
  [[ "$n" -ge 2 ]] && emit critical "[SLO] RPC p95 代理面 ${RPC_MS}ms >10s（持续 2 拍）"
else
  reset rpc_p95
fi

# ④ cron+备份 WARN——backup.log 尾 WARN/ERROR 行即 trigger
# ④a 备份新鲜度死闸（cron 死/脚本哑/日志缺三型兜底——OK 龄>30h=critical；
# AlertDedupKey 数值归一 <N> 天然 dedup 不手写等值）。
BACKUP_LOG=/opt/nox-backups/backup.log
if [[ ! -f "$BACKUP_LOG" ]]; then
  emit critical "[SLO] backup.log 缺——备份链全灭不可见域"
else
  OK_TS=$(grep 'backup OK' "$BACKUP_LOG" | tail -1 | grep -oP '\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}' | head -1)
  if [[ -z "$OK_TS" ]]; then
    emit critical "[SLO] backup.log 零 backup OK 行——备份从未成功域"
  else
    OK_EPOCH=$(TZ=UTC date -d "$OK_TS" +%s 2>/dev/null || echo 0)
    AGE_S=$(( $(date -u +%s) - OK_EPOCH ))
    [[ "$AGE_S" -gt 108000 ]] && emit critical "[SLO] backup OK 龄 ${AGE_S}s >30h——备份死晨域"
  fi
fi
if [[ -f /opt/nox-backups/backup.log ]]; then
  LAST=$(tail -20 /opt/nox-backups/backup.log 2>/dev/null | grep -c 'WARN\|ERROR' || true)
  [[ "$LAST" -gt 0 ]] && emit warn "[SLO] backup.log WARN/ERROR 行在尾窗（${LAST} 条——备份降级面）"
fi
