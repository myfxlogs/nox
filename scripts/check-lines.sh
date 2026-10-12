#!/usr/bin/env bash
# 文件规模门禁（AGENTS.md §7 代码规模 / D-077 修复⑩ G7）：
# Go/TS 源文件 >450 行硬阻断（exit 1）、>300 行软警告；豁免 proto 生成代码与测试文件。
# 由 pre-commit 调用（只查暂存文件），也可单独跑（查全仓）。
# CI 同步执行（与 pre-commit 预算一致）——CI 模式查全仓但豁免既有超标文件（渐进收紧）。
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
SOFT=300
HARD=450

# exempt：proto 生成代码（internal/adapter/gen/、internal/gen/、internal/dashboard/gen/ 旧位残留兼容）与 *_test.go
# + docs/ 下的 .go 证据探针文件（//go:build ignore，非生产代码）
exempt() {
  case "$1" in
    internal/adapter/gen/*|internal/gen/*|internal/dashboard/gen/*|web/src/gen/*|*_test.go) return 0 ;;
    docs/*.go) return 0 ;;
    # 一次性建库工具（D-274 S2 backfill，2026-09-15 使命已完成 INSERT 已执行；
    # 非 arb-core 生产代码，类 docs/*.go 证据探针。禁新增长期驻留工具到此豁免——Claude 决策席 2026-09-16）
    tools/d274-backfill/*) return 0 ;;
  esac
  return 1
}

# 既有超标文件白名单（D-077 修复⑩ G7：渐进收紧，新文件不进白名单）
# 这些文件超标是历史技术债，后续逐个拆分后从白名单移除。
# 2026-08-22 全部拆分完毕（修复⑭返修包①续：21 文件全 ≤300），白名单清空。
LEGACY_OVERSIZE=()

is_legacy_oversize() {
  local rel="$1"
  for f in "${LEGACY_OVERSIZE[@]}"; do
    [ "$rel" = "$f" ] && return 0
  done
  return 1
}

hard=0 soft=0
while IFS= read -r f; do
  rel="${f#"$ROOT"/}"
  exempt "$rel" && continue
  n="$(wc -l < "$f")"
  if [ "$n" -gt "$HARD" ]; then
    if is_legacy_oversize "$rel"; then
      echo "⚠️  $rel: $n 行（> $HARD 既有超标，白名单豁免，待拆分）"
      soft=1
    else
      echo "❌ $rel: $n 行（> $HARD 硬阻断）"
      hard=1
    fi
  elif [ "$n" -gt "$SOFT" ]; then
    echo "⚠️  $rel: $n 行（> $SOFT 警告）"
    soft=1
  fi
done < <(find "$ROOT" -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' \) \
  -not -path '*/node_modules/*' -not -path '*/.git/*' -not -path '*/ref/*')

if [ "$hard" -eq 1 ]; then
  echo "❌ check-lines: 存在 >$HARD 行的源文件，提交被阻断"
  exit 1
fi
if [ "$soft" -eq 1 ]; then
  echo "⚠️  check-lines: 存在 >$SOFT 行的源文件（软警告，建议拆分）"
fi
exit 0
