#!/usr/bin/env bash
# D-496 §4 迁移纪律门禁（业主令 9-26 追加，防整改后漂移）。
# 对 staged 的 migrations/*.sql 新文件逐条扫描：
#   ① 新增 venue/account/symbol/venue_type 键域列必须同文件带 COMMENT ON COLUMN（域归属声明）
#   ② 新增 venue 类列（venue/venue_*/mt_venue/cex_venue）必须带 CHECK 域约束（裸域禁冒号或枚举闭集）
#   ③ 禁建新 *_backup_* / *_tmp 表进主 schema
# 退出码 0=过；1=违规（打印缺失清单）。
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# 只查本次 staged 新增的迁移文件
FILES=$(git -c core.quotepath=off diff --cached --name-only --diff-filter=A -- 'migrations/*.sql' || true)
# 非 git 调用场景（手动跑）：可传文件名参数
if [ $# -gt 0 ]; then FILES="$*"; fi

# ④ 迁移号唯一性双检（D-561 撞号实证——同号后登记者被迁移追踪 silent-skip=闸列不落库）：
#    a) 文件名级：staged 新迁移 NNNN_*.sql 与 migrations/ 顶层既有并集同号 → FAIL
#    b) 登记表级：migrate_table.go 行首 {NNNN, 版本号重复 → FAIL（不限本提交——撞号伤害在并轨时）
#    replay/ 子目录独立号域不染。
uniq_fail=0
# a) 文件名级：migrations/ 顶层 NNNN_*.sql 按号聚合查重（staged+已提交并集=ls 域）
dup_files=$(ls migrations/*.sql 2>/dev/null | xargs -n1 basename | grep -oE '^[0-9]{3,4}' | sort | uniq -d || true)
if [ -n "$dup_files" ]; then
  echo "FAIL migrations/: 迁移号文件名撞号（$(echo "$dup_files" | paste -sd' ' -)）——同号后登记者会被 silent-skip" >&2
  uniq_fail=1
fi
# b) 登记表级：migrate_table.go 行首 {NNNN, 版本号查重
REG="internal/store/pgstore/migrate_table.go"
if [ -f "$REG" ]; then
  dup_reg=$(grep -oE '^\s*\{[0-9]+,' "$REG" | grep -oE '[0-9]+' | sort -n | uniq -d || true)
  if [ -n "$dup_reg" ]; then
    echo "FAIL $REG: 版本号重复登记（$(echo "$dup_reg" | paste -sd' ' -)）——两文件占同号必有一方被跳" >&2
    uniq_fail=1
  fi
fi

if [ -z "$FILES" ]; then exit $uniq_fail; fi

fail=$uniq_fail
for f in $FILES; do
  [ -f "$f" ] || continue
  # ③ 死表/临时表新建禁令
  if grep -inE 'CREATE TABLE[^;]*(_backup_|_tmp|_temp)([^a-z]|$)' "$f" >/dev/null; then
    echo "FAIL $f: 禁建 *_backup_*/_tmp 表（备份残留病灶即由此来）" >&2; fail=1
  fi
  # 收集 ADD COLUMN / CREATE TABLE 里引入的键域列名 → 逐列查 COMMENT
  cols=$(grep -oiE '(ADD COLUMN[^(]*|COMMENT ON COLUMN)' "$f" >/dev/null 2>&1; \
    awk 'BEGIN{IGNORECASE=1}
      /ADD COLUMN|CREATE TABLE/{
        line=$0
        while (match(line, /(ADD COLUMN +[a-zA-Z_][a-zA-Z0-9_]*|[,(] *[a-zA-Z_][a-zA-Z0-9_]* +(text|bigint|int|integer|double|numeric|timestamptz|timestamp|boolean|bool))/)) {
          seg=substr(line, RSTART, RLENGTH)
          gsub(/ADD COLUMN/, "", seg); gsub(/[,(]/, "", seg)
          gsub(/^ +| +$/, "", seg); split(seg, a, / +/); col=a[1]
          if (col ~ /^(venue|venue_.*|.*_venue|account|account_.*|symbol|venue_type.*|.*_symbol)$/)
            print FILENAME "|" col
          line=substr(line, RSTART+RLENGTH)
        }
      }' "$f" | sort -u)
  flat=$(tr '\n' ' ' < "$f")
  for c in $cols; do
    col="${c##*|}"
    grep -qiE "COMMENT ON COLUMN +[a-zA-Z_0-9]+\.$col +IS" "$f" || {
      echo "FAIL $f: 键域列 $col 缺 COMMENT ON COLUMN 域归属声明（§4 自说明条款）" >&2; fail=1; }
    case "$col" in
      venue_key*|*_venue_key) ;; # R3 生成投影列本身就含 ':'，不套裸域 CHECK
      venue_type*|*_venue_type)
        grep -qiE "$col +IN *\(" <<< "$flat" || {
          echo "FAIL $f: venue_type 类列 $col 缺枚举闭集 CHECK 约束" >&2; fail=1; } ;;
      venue|venue_buy|venue_sell|mt_venue|cex_venue|venue_a|venue_b|*_venue)
        grep -qiE "$col[^;]*NOT LIKE '%:%'" <<< "$flat" || {
          echo "FAIL $f: venue 类列 $col 缺裸域 CHECK（禁冒号）约束" >&2; fail=1; } ;;
    esac
  done
done
exit $fail
