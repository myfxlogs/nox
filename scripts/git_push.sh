#!/usr/bin/env bash
# git_push.sh —— 带重试的推送包装器（D-588：裸 git push 瞬时失败静默滞留病灶修）。
# 用法：scripts/git_push.sh [remote] [refspec]——缺省 nox HEAD:master。
# 3 次指数退避；终败非零退出+醒目报错（调用方/复审席须处置，禁吞）。
set -uo pipefail
REMOTE="${1:-nox}"
REFSPEC="${2:-HEAD:master}"
for i in 1 2 3; do
  if git push "$REMOTE" "$REFSPEC" 2>&1; then
    exit 0
  fi
  echo "⚠️  git push 第 $i 次失败，$((i*3))s 后重试…" >&2
  sleep $((i*3))
done
echo "❌ git push $REMOTE $REFSPEC 连续 3 次失败——本地 commit 滞留。" >&2
echo "   处置：查网络/远端权限；恢复后重跑本脚本。pre-commit 会持续 warn 直至推送成功。" >&2
exit 1
