#!/usr/bin/env bash
# check_stale_base.sh —— D-651-B②/㊁：stale 基探针（提示件非阻断件）。
# 三撞 stale 基合并冲突+STATE 滞翻复派病灶根治：席位本地基落后 nox/master
# 时读的是陈票。git fetch 败=如实报非静默；落后>0→非零退出+warn 文案；
# base=当前分支 merge-base nox/master（worktree/wip 分支同覆盖）。
set -uo pipefail
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if ! git fetch nox master --quiet 2>/dev/null; then
	echo "[stale-base] 远端 nox 不可达（fetch 败）——如实报出非静默" >&2
	exit 0 # 只报不拦：网络域不可达非基陈旧证据，不阻断
fi
base=$(git merge-base HEAD nox/master 2>/dev/null || true)
if [ -z "$base" ]; then
	echo "[stale-base] merge-base 缺（基线未接）" >&2
	exit 0
fi
behind=$(git rev-list --count "$base..nox/master" 2>/dev/null || echo 0)
if [ "$behind" -gt 0 ]; then
	echo "[stale-base] 本地基落后远端 $behind commits——先 pull 再动（陈票复派/冲突双病灶源）" >&2
	exit 1
fi
exit 0
