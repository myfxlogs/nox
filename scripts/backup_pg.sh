#!/usr/bin/env bash
# backup_pg.sh D-159 W6a S3：PostgreSQL 每日备份（pg_dump + gzip + sha256 + retention 7 天）。
# D-161 补偿措施⑤：仓库外目录+权限 600+checksum+不进 git/docs/LOG/聊天输出。
# D-110 第三落点：age 加密 → rclone 上传 R2 → R2 侧滚动 14 份，消除双机同灭 DR 缺口。
#   配置缺失=降级「仅本地+warn」；加密/上传失败=alerts critical（D-033 自盯）+本地全保留；
#   fail-open 只许在上传段，本地链路任何情况下不阻不断。
#
# 用法：scripts/backup_pg.sh [PG_DSN]
# 默认 PG_DSN 端口取 nox-postgres 运行容器发布值（D-114：宿主拓扑权威源；5433=化石端口勿写死）。
# 备份目录：/opt/nox-backups/（权限 600，仓库外；env BACKUP_DIR 可覆盖——仅测试/演练用，cron 不设=生产路径）
# R2 外推配置：$BACKUP_DIR/backup-r2.env（仓库外 600，含 AGE_PUBKEY/R2_REMOTE/R2_BUCKET；私钥业主离线持有）
# crontab：0 3 * * * /opt/nox/scripts/backup_pg.sh >> /opt/nox-backups/backup.log 2>&1

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/opt/nox-backups}"
if [ $# -ge 1 ]; then
	PG_DSN="$1"
else
	NOX_PG_PORT="$(docker port nox-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
	[ -n "$NOX_PG_PORT" ] || { echo "[$(date -u +%FT%TZ)] backup FAIL: nox-postgres 端口解析失败（传 PG_DSN 显式旁路）" >&2; exit 1; }
	PG_DSN="postgres://nox:nox@localhost:${NOX_PG_PORT}/nox?sslmode=disable"
fi
RETENTION_DAYS=13
R2_KEEP=14
STATUS=0

mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"

# D-110 告警：alerts critical（系统级，rule_id NULL=D-027）；psql 不可达 → log 兜底，不带崩本地备份。
# message 只用固定文案（阶段+原因），不含 DSN/凭据/外部输入，SQL 字面量注入面为零。
emit_alert() {
	if psql "$PG_DSN" -q -v ON_ERROR_STOP=1 \
		-c "INSERT INTO alerts (ts, level, message, acked) VALUES (now(), 'critical', '$1', false);" >/dev/null 2>&1; then
		echo "[$(date -u +%FT%TZ)] alert critical inserted: $1"
	else
		echo "[$(date -u +%FT%TZ)] ALERT(critical, psql 不可达兜底): $1" >&2
	fi
}

# D-110：读 R2 外推配置。文件缺失/不可读/源失败/任一变量空 → 降级：跳过加密与上传，warn，本地照常。
# 配置在而 age/rclone 不可用不算降级——走 critical（DR 缺口无声打开比降级更危险）。
R2_ENV_FILE="$BACKUP_DIR/backup-r2.env"
R2_CONFIGURED=0
AGE_PUBKEY="" R2_REMOTE="" R2_BUCKET=""
if [ -f "$R2_ENV_FILE" ] && . "$R2_ENV_FILE" \
	&& [ -n "$AGE_PUBKEY" ] && [ -n "$R2_REMOTE" ] && [ -n "$R2_BUCKET" ]; then
	R2_CONFIGURED=1
	echo "[$(date -u +%FT%TZ)] r2 offload enabled: bucket=$R2_BUCKET keep=$R2_KEEP"
else
	echo "[$(date -u +%FT%TZ)] WARN: $R2_ENV_FILE missing/incomplete, degrade to local-only backup" >&2
	emit_alert "[backup] R2 外推配置缺失→local-only 降级（三地备份实质单地——安全垫失守，需业主配给 backup-r2.env：AGE_PUBKEY/R2_REMOTE/R2_BUCKET）"
fi


# R2 侧滚动：nox_*.sql.gz.age 按对象时间序保留最新 R2_KEEP 份，删最旧（rclone lsl/deletefile）。
r2_rotate() {
	local listing total excess obj deleted=0
	if ! listing=$(rclone lsl "$R2_REMOTE:$R2_BUCKET/" | awk 'NF>=4 && $NF ~ /^nox_/ && $NF ~ /\.sql\.gz\.age$/'); then
		echo "[$(date -u +%FT%TZ)] WARN: rclone lsl failed, skip R2 rotation" >&2
		return 0
	fi
	total=0
	if [ -n "$listing" ]; then
		total=$(printf '%s\n' "$listing" | wc -l)
	fi
	[ "$total" -gt "$R2_KEEP" ] || return 0
	excess=$((total - R2_KEEP))
	while read -r obj; do
		[ -n "$obj" ] || continue
		if rclone deletefile "$R2_REMOTE:$R2_BUCKET/$obj"; then
			deleted=$((deleted + 1))
		else
			echo "[$(date -u +%FT%TZ)] WARN: rclone deletefile failed: $obj" >&2
		fi
	done < <(printf '%s\n' "$listing" | LC_ALL=C sort -k2,2 -k3,3 | head -n "$excess" | awk '{print $NF}')
	echo "[$(date -u +%FT%TZ)] r2 rotation: total=$total kept=$((total - deleted)) deleted=$deleted (limit $R2_KEEP)"
}

TIMESTAMP=$(date -u +"%Y%m%d_%H%M%S")
DUMP_FILE="$BACKUP_DIR/nox_${TIMESTAMP}.sql.gz"
SHA_FILE="$DUMP_FILE.sha256"

echo "[$(date -u +%FT%TZ)] backup start: $DUMP_FILE"

# pg_dump | gzip（管道避免明文落盘）
if ! pg_dump "$PG_DSN" | gzip > "$DUMP_FILE"; then
	echo "[$(date -u +%FT%TZ)] backup FAIL: pg_dump error" >&2
	rm -f "$DUMP_FILE"
# D-277 C5：备份保留 14 份轮转（~80MB/日无界增长实证，D-213 增长源硬顶原则）
ls -1t "$BACKUP_DIR"/nox_*.sql.gz 2>/dev/null | tail -n +15 | xargs -r rm -f
	exit 1
fi

chmod 600 "$DUMP_FILE"

# checksum
sha256sum "$DUMP_FILE" > "$SHA_FILE"
chmod 600 "$SHA_FILE"

# retention 7 天
find "$BACKUP_DIR" -name "nox_*.sql.gz" -mtime +${RETENTION_DAYS} -delete
find "$BACKUP_DIR" -name "nox_*.sql.gz.sha256" -mtime +${RETENTION_DAYS} -delete
# D-110：本地密文同窗滚动（上传段产物不无界落盘，D-213 同源）
find "$BACKUP_DIR" -name "nox_*.sql.gz.age" -mtime +${RETENTION_DAYS} -delete

SIZE=$(stat -c%s "$DUMP_FILE" 2>/dev/null || stat -f%z "$DUMP_FILE")
echo "[$(date -u +%FT%TZ)] backup OK: $DUMP_FILE (${SIZE} bytes), sha256=$SHA_FILE"

# ---- D-110 第三落点：age 加密 → rclone 上传 R2 → R2 滚动（失败本地全保留，STATUS 只反映外推段）----
if [ "$R2_CONFIGURED" -eq 1 ]; then
	AGE_FILE="$DUMP_FILE.age"
	if ! age -r "$AGE_PUBKEY" -o "$AGE_FILE" "$DUMP_FILE"; then
		echo "[$(date -u +%FT%TZ)] encrypt FAIL: age error, local dump kept" >&2
		emit_alert "[backup] age encrypt failed, local dump kept (D-110)"
		STATUS=1
	else
		chmod 600 "$AGE_FILE"
		if ! rclone copy "$AGE_FILE" "$R2_REMOTE:$R2_BUCKET/"; then
			echo "[$(date -u +%FT%TZ)] upload FAIL: rclone error, local kept" >&2
			emit_alert "[backup] rclone upload R2 failed, local kept (D-110)"
			STATUS=1
		else
			AGE_SIZE=$(stat -c%s "$AGE_FILE" 2>/dev/null || stat -f%z "$AGE_FILE")
			R2_SHA=$(awk '{print $1}' "$SHA_FILE")
			echo "[$(date -u +%FT%TZ)] rclone OK: $R2_REMOTE:$R2_BUCKET/$(basename "$AGE_FILE") (${AGE_SIZE} bytes), sha256(dump)=$R2_SHA"
			r2_rotate
		fi
	fi
fi

# 机器报告备份（D-001：symdig/sysretro 不入 git）
mkdir -p "$BACKUP_DIR/reports" && rsync -a --delete "$(dirname "$0")/../docs/报告/" "$BACKUP_DIR/reports/"

exit "$STATUS"
