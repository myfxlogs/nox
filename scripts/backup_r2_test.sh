#!/usr/bin/env bash
# backup_r2_test.sh D-110：backup_pg.sh 第三落点（age 加密 → rclone R2 上传 → R2 滚动）stub 测试。
# 全程 PATH stub（pg_dump/age/rclone/psql/rsync）+ BACKUP_DIR 指向临时目录：
# 不触生产库、不触 /opt/arb-backups、无需安装 age/rclone。
# 覆盖施工提示词四锚 + 两个延伸面：配置残缺降级、psql 不可达 log 兜底。
# 输出 BACKUP_R2_TEST_OK 或首个失败断言。

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
TARGET="$SCRIPT_DIR/backup_pg.sh"
FAKE_DSN="postgres://stub:stub@127.0.0.1:1/stub?sslmode=disable"

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
STUB="$ROOT/stub"
mkdir -p "$STUB"

# ---- stubs（行为由 env 开关驱动；所有调用落日志供断言）----
cat > "$STUB/pg_dump" <<'EOF'
#!/usr/bin/env bash
echo "CREATE TABLE stub_d110 (i int);"
echo "COPY stub_d110 (i) FROM stdin;"
echo "1"
echo "\\."
EOF
cat > "$STUB/rsync" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$STUB/psql" <<'EOF'
#!/usr/bin/env bash
if [ "${PSQL_FAIL:-0}" = "1" ]; then
	echo "psql: could not connect to server (stub)" >&2
	exit 1
fi
printf '%s\n' "$*" >> "$PSQL_LOG"
exit 0
EOF
cat > "$STUB/age" <<'EOF'
#!/usr/bin/env bash
out=""; src=""; prev=""
for a in "$@"; do
	case "$prev" in -o) out="$a" ;; esac
	case "$a" in -r|-o) ;; *) src="$a" ;; esac
	prev="$a"
done
if [ "${AGE_FAIL:-0}" = "1" ]; then
	echo "age: stub encryption failure" >&2
	exit 1
fi
cp "$src" "$out"
EOF
cat > "$STUB/rclone" <<'EOF'
#!/usr/bin/env bash
printf 'rclone %s\n' "$*" >> "$RCLONE_LOG"
case "$1" in
	copy)
		[ "${RCLONE_COPY_FAIL:-0}" = "1" ] && { echo "rclone copy: stub failure" >&2; exit 1; }
		;;
	lsl)
		[ -n "${RCLONE_LSL_FILE:-}" ] && cat "$RCLONE_LSL_FILE"
		;;
	deletefile)
		printf '%s\n' "$2" >> "$RCLONE_DEL_LOG"
		;;
esac
exit 0
EOF
chmod +x "$STUB"/pg_dump "$STUB"/rsync "$STUB"/psql "$STUB"/age "$STUB"/rclone

export PATH="$STUB:$PATH"
export PSQL_LOG="$ROOT/psql.log" RCLONE_LOG="$ROOT/rclone.log" RCLONE_DEL_LOG="$ROOT/del.log"
: > "$PSQL_LOG"; : > "$RCLONE_LOG"; : > "$RCLONE_DEL_LOG"

# ---- 小工具 ----
run_case() { # $1=case 名（BACKUP_DIR=$ROOT/<名>，调用方自设行为开关）
	BDIR="$ROOT/$1"; mkdir -p "$BDIR"
	: > "$PSQL_LOG"; : > "$RCLONE_LOG"; : > "$RCLONE_DEL_LOG"
	set +e
	OUT=$(BACKUP_DIR="$BDIR" bash "$TARGET" "$FAKE_DSN" 2>&1)
	RC=$?
	set -e
}
write_env() { # $1=dir $2=AGE_PUBKEY $3=R2_REMOTE $4=R2_BUCKET
	mkdir -p "$1"
	printf 'AGE_PUBKEY=%s\nR2_REMOTE=%s\nR2_BUCKET=%s\n' "$2" "$3" "$4" > "$1/backup-r2.env"
}
assert_eq() { [ "$2" = "$3" ] || { echo "FAIL[$1]: got=[$2] want=[$3]"; echo "$OUT"; exit 1; }; }
assert_has() { grep -q "$2" <<<"$OUT" || { echo "FAIL[$1]: output missing [$2]"; echo "$OUT"; exit 1; }; }
assert_lacks() { grep -q "$2" <<<"$OUT" && { echo "FAIL[$1]: output unexpected [$2]"; echo "$OUT"; exit 1; } || :; }
assert_file_n() { # $1=case $2=glob $3=期望份数
	local n
	n=$(find "$BDIR" -maxdepth 1 -name "$2" 2>/dev/null | wc -l)
	[ "$n" = "$3" ] || { echo "FAIL[$1]: glob $2 count=$n want=$3"; ls -la "$BDIR"; exit 1; }
}

# ============ 锚①：配置缺失 → 本地照常 + warn + 零上传零告警 ============
unset AGE_FAIL RCLONE_COPY_FAIL PSQL_FAIL RCLONE_LSL_FILE || :
run_case c1_no_env
assert_eq c1 "$RC" 0
assert_has c1 "degrade to local-only backup"
assert_file_n c1 "arb_*.sql.gz" 1
assert_file_n c1 "arb_*.sql.gz.sha256" 1
assert_file_n c1 "arb_*.sql.gz.age" 0
[ -s "$RCLONE_LOG" ] && { echo "FAIL[c1]: rclone 被调用"; cat "$RCLONE_LOG"; exit 1; }
[ -s "$PSQL_LOG" ] && { echo "FAIL[c1]: 告警误发"; cat "$PSQL_LOG"; exit 1; }

# 延伸：配置在但变量残缺 → 同样降级
write_env "$ROOT/c1b_partial" "age1pubkey" "" "arb-backup"
run_case c1b_partial
assert_eq c1b "$RC" 0
assert_has c1b "degrade to local-only backup"
[ -s "$RCLONE_LOG" ] && { echo "FAIL[c1b]: rclone 被调用"; exit 1; }

# ============ 锚②：坏公钥（age 失败）→ critical + 本地 dump 保留 ============
BDIR="$ROOT/c2_age_fail"; mkdir -p "$BDIR"
write_env "$BDIR" "age1badpubkey" "r2remote" "arb-backup"
export AGE_FAIL=1
run_case c2_age_fail
unset AGE_FAIL
assert_eq c2 "$RC" 1
assert_file_n c2 "arb_*.sql.gz" 1
assert_file_n c2 "arb_*.sql.gz.sha256" 1
assert_file_n c2 "arb_*.sql.gz.age" 0
grep -q "INSERT INTO alerts" "$PSQL_LOG" || { echo "FAIL[c2]: critical 未入 alerts"; cat "$PSQL_LOG"; exit 1; }
grep -q "'critical'" "$PSQL_LOG" || { echo "FAIL[c2]: 告警级别非 critical"; exit 1; }
grep -q "age encrypt failed" "$PSQL_LOG" || { echo "FAIL[c2]: 告警缺阶段原因"; exit 1; }
[ -s "$RCLONE_LOG" ] && { echo "FAIL[c2]: 加密失败仍尝试上传"; exit 1; }

# ============ 锚③：rclone 上传非零退出 → critical + 本地全保留 ============
BDIR="$ROOT/c3_upload_fail"; mkdir -p "$BDIR"
write_env "$BDIR" "age1pubkey" "r2remote" "arb-backup"
export RCLONE_COPY_FAIL=1
run_case c3_upload_fail
unset RCLONE_COPY_FAIL
assert_eq c3 "$RC" 1
assert_file_n c3 "arb_*.sql.gz" 1
assert_file_n c3 "arb_*.sql.gz.age" 1
assert_file_n c3 "arb_*.sql.gz.sha256" 1
grep -q "rclone upload R2 failed" "$PSQL_LOG" || { echo "FAIL[c3]: critical 未入 alerts"; cat "$PSQL_LOG"; exit 1; }

# 延伸：psql 不可达 → log 兜底，脚本不被带崩（本地链路完整、退出码仍反映外推失败）
BDIR="$ROOT/c3b_psql_down"; mkdir -p "$BDIR"
write_env "$BDIR" "age1pubkey" "r2remote" "arb-backup"
export RCLONE_COPY_FAIL=1 PSQL_FAIL=1
run_case c3b_psql_down
unset RCLONE_COPY_FAIL PSQL_FAIL
assert_eq c3b "$RC" 1
assert_has c3b "ALERT(critical"
assert_has c3b "rclone upload R2 failed"
[ -s "$PSQL_LOG" ] && { echo "FAIL[c3b]: psql 挂了还留了记录"; exit 1; }
assert_file_n c3b "arb_*.sql.gz" 1
assert_file_n c3b "arb_*.sql.gz.age" 1

# ============ 锚④：R2 列表 16 项 → 仅删最旧 2 项 ============
BDIR="$ROOT/c4_rotate"; mkdir -p "$BDIR"
write_env "$BDIR" "age1pubkey" "r2remote" "arb-backup"
LSL="$ROOT/lsl.txt"
: > "$LSL"
i=1
while [ "$i" -le 16 ]; do
	d=$(printf '%02d' "$i")
	printf '%9d 2026-09-%s 03:00:01.000000000 arb_202609%s_030001.sql.gz.age\n' "$((i * 1000))" "$d" "$d" >> "$LSL"
	i=$((i + 1))
done
export RCLONE_LSL_FILE="$LSL"
run_case c4_rotate
unset RCLONE_LSL_FILE
assert_eq c4 "$RC" 0
assert_has c4 "rclone OK: r2remote:arb-backup/"
assert_has c4 "sha256(dump)="
assert_has c4 "r2 rotation: total=16 kept=14 deleted=2"
[ "$(wc -l < "$RCLONE_DEL_LOG")" = "2" ] || { echo "FAIL[c4]: 删除数非 2"; cat "$RCLONE_DEL_LOG"; exit 1; }
grep -q "arb_20260901_030001.sql.gz.age" "$RCLONE_DEL_LOG" || { echo "FAIL[c4]: 最旧①未删"; exit 1; }
grep -q "arb_20260902_030001.sql.gz.age" "$RCLONE_DEL_LOG" || { echo "FAIL[c4]: 最旧②未删"; exit 1; }
grep -q "arb_20260903_" "$RCLONE_DEL_LOG" && { echo "FAIL[c4]: 误删非最旧对象"; exit 1; }
[ -s "$PSQL_LOG" ] && { echo "FAIL[c4]: 成功路径误发告警"; cat "$PSQL_LOG"; exit 1; }

echo "BACKUP_R2_TEST_OK（四锚+2 延伸全过）"
