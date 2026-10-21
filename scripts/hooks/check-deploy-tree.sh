#!/usr/bin/env bash
# check-deploy-tree.sh —— D-651 B① 部署树写禁执行器化
# （契约「/opt/nox 只 pull/build/deploy，禁开发」的 pre-commit 执行器——
# 没执行器的纪律=建议）。
#
# 判据（复审补正㊀定稿，替代「只容 merge/deploy 产生的 M」不可归因形）：
#   - 非部署树（git root realpath != /opt/nox）→ 直通（worktree/开发树零干预）；
#   - MERGE_HEAD 在途（merge commit 进行中）→ 放行（部署合并=部署树合法写径）；
#   - 当前分支 != master → 拦（wip/临时分支在部署树禁 commit——检出 wip 即警告态）；
#   - 暂存面含 docs/** 以外路径 → 拦（代码/迁移/配置改动一律走 worktree）；
#   - master + 纯 docs/** 暂存（STATE/TICKETS/决策档）→ warn 放行
#     （部署树台账簿写是常态运维径，非开发写）。
set -uo pipefail

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
[ "$(realpath "$root" 2>/dev/null)" = "/opt/nox" ] || exit 0

# merge commit 进行中=部署合并合法径（git merge/pull 的 commit 前置放行）。
if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
  exit 0
fi

branch=$(git branch --show-current 2>/dev/null || true)
if [ "$branch" != "master" ]; then
  echo "FAIL(D-651 B①): 部署树 /opt/nox 检出分支=$branch（非 master）禁 commit——" >&2
  echo "  开发走 worktree：git worktree add /opt/nox-wt/<票> -b wip/<票>" >&2
  exit 1
fi

staged=$(git -c core.quotepath=off diff --cached --name-only 2>/dev/null || true)
[ -z "$staged" ] && exit 0
bad=$(printf '%s\n' "$staged" | grep -v '^docs/' || true)
if [ -n "$bad" ]; then
  echo "FAIL(D-651 B①): 部署树 master 仅容 docs/** 台账写——以下路径须 worktree 径：" >&2
  printf '%s\n' "$bad" | head -10 >&2
  exit 1
fi
echo "WARN(D-651 B①): 部署树纯 docs 提交放行——代码/配置改动请走 /opt/nox-wt/<票>" >&2
exit 0
