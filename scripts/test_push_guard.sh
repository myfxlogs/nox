#!/usr/bin/env bash
# test_push_guard.sh —— push 陷阱根治行为测试（库卫生小包 10-19）。
# ①hook 直调：远端名闸（nox 过 / 非法名拒+指路）；②temp 仓端到端：
# git_push.sh 缺省推通 / origin 缺自动回落 nox / 两远端皆缺显式失败。
# 用法：scripts/test_push_guard.sh（repo 根任意 cwd；零网络，全部本地 temp 仓）。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOOK="$ROOT/scripts/hooks/pre-push"
PUSH="$ROOT/scripts/git_push.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0
ck() { # ck <名> <期望0/1> <命令...>
  local name="$1" want="$2"; shift 2
  if "$@" >/dev/null 2>&1; then got=0; else got=$?; fi
  if [ "$got" = "$want" ]; then echo "PASS: $name"; else echo "FAIL: $name（exit=$got 期望=$want）"; fail=1; fi
}
ckmsg() { # ckmsg <名> <命令串（eval，stderr 合流）> <须含子串>
  local name="$1" cmd="$2" want="$3" out
  out=$(eval "$cmd" 2>&1 >/dev/null || true)
  case "$out" in
    *"$want"*) echo "PASS: $name";;
    *) echo "FAIL: $name（输出缺「$want」：${out:0:120}）"; fail=1;;
  esac
}

echo "== ① pre-push 远端名闸（hook 直调，空 stdin）"
ck "hook nox 空 stdin 放行" 0 bash "$HOOK" nox </dev/null
ck "hook 非法远端拒绝" 1 bash "$HOOK" origin </dev/null
ckmsg "hook 拒绝输出含指路" "bash '$HOOK' origin </dev/null" "统一走 scripts/git_push.sh"

echo "== ② git_push.sh 端到端（temp 裸仓）"
git init -q --bare "$TMP/up.git"
git init -q "$TMP/wt"
git -C "$TMP/wt" -c user.email=t@t -c user.name=t commit -q --allow-empty -m seed
git -C "$TMP/wt" remote add nox "$TMP/up.git"
# 缺省远端（nox）推通
ck "git_push.sh 缺省 nox 推通" 0 bash -c "cd '$TMP/wt' && bash '$PUSH' nox HEAD:refs/heads/t1"
git -C "$TMP/up.git" rev-parse --verify -q refs/heads/t1 >/dev/null 2>&1 \
  && echo "PASS: t1 落裸仓" || { echo "FAIL: t1 未落裸仓"; fail=1; }
# 请求 origin（不存在）自动回落 nox
ck "git_push.sh origin 缺自动回落" 0 bash -c "cd '$TMP/wt' && bash '$PUSH' origin HEAD:refs/heads/t2"
git -C "$TMP/up.git" rev-parse --verify -q refs/heads/t2 >/dev/null 2>&1 \
  && echo "PASS: t2 经回落落裸仓" || { echo "FAIL: t2 未落裸仓"; fail=1; }
# 两远端皆缺→显式失败（clone2 只有别名 up，无 nox 无 origin）
git init -q "$TMP/wt2"
git -C "$TMP/wt2" -c user.email=t@t -c user.name=t commit -q --allow-empty -m seed
git -C "$TMP/wt2" remote add up "$TMP/up.git"
ck "git_push.sh 双缺显式失败" 1 bash -c "cd '$TMP/wt2' && bash '$PUSH' origin HEAD:refs/heads/t3"
ckmsg "双缺输出含远端清单指路" "cd '$TMP/wt2' && bash '$PUSH' origin HEAD:refs/heads/t3" "远端清单"

echo "== 结果：$([ $fail = 0 ] && echo ALL_PASS || echo HAS_FAIL)"
exit $fail
