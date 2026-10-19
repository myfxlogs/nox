#!/usr/bin/env bash
# git_push.sh —— 带重试的推送包装器（D-588：裸 git push 瞬时失败静默滞留病灶修）。
# 用法：scripts/git_push.sh [remote] [refspec]——缺省 nox HEAD:master。
# 3 次指数退避；终败非零退出+醒目报错（调用方/复审席须处置，禁吞）。
# 库卫生小包（10-19）：push 陷阱根治——仓库唯一远端名=nox（无 origin），
# 裸 `git push origin` 静默败曾吞件（6f4a396→cherry-pick 6407ec0 回补）。
# 本包装入即唯一推送入口：请求远端不存在而 nox 在→告警自动改推 nox；
# 两远端皆缺→显式失败指路，绝不静默。
set -uo pipefail
REMOTE="${1:-nox}"
REFSPEC="${2:-HEAD:master}"
if ! git remote get-url "$REMOTE" >/dev/null 2>&1; then
  if [ "$REMOTE" != "nox" ] && git remote get-url nox >/dev/null 2>&1; then
    echo "⚠️  远端 '$REMOTE' 不存在（本仓唯一远端=nox）——自动改推 nox。祖案：6f4a396 被裸 push origin 静默吞件。" >&2
    REMOTE="nox"
  else
    echo "❌ 远端 '$REMOTE' 不存在且无 nox 可回落——本仓远端清单：" >&2
    git remote -v >&2
    exit 1
  fi
fi
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
