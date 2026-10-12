---
schema_version: 2
id: git-commit-pipe-masks-failure
kind: pitfall
risk: high
status: verified
keywords: [git commit, 管道吞退出码, tail, and链短路, 空分支推送, pre-commit 拦截, 静默失败, push 前后双锚验证, rev-parse, 假绿]
scope: [多门禁仓库的 git 工作流, "commit 接管道转 tail 的写法", seat 分支推送纪律]
summary: 写法 git commit -m 加管道转 tail 再用 and 链接 push 时，退出码取自 tail 而非 git——pre-commit 门禁 FAIL 时 commit 未创建，&&链照常执行把空分支/旧 HEAD 推上远端，"推送成功"输出掩盖交付静默丢失。修法：①commit 后不得接管道，或 set -o pipefail；②push 前后各验证一次 `git rev-parse HEAD` vs `git ls-remote`（两次实锚，锚点不相等=没推上）；③commit 产物用 `git log --oneline -1` 看哈希前进。本仓两天内两案例（mro 交付空分支漂一夜、D-273 施工包首次提交被 STATE 门禁拦）。
first_seen: 2026-09-09
last_verified: 2026-09-22
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: doc
    ref: docs/经验库/条目/commit-payload-message-mismatch.md（同家族：提交载荷与信息不符）
  - type: server_fact
    source: git
    query: "seat/zcode-mro 曾推在 5a4b896c（空分支）而交付 33cbb3f3 次日才落库；seat/zcode-d273 首推 666c1041（空）后被 5c909e62 纠正；seat/devin-d301c/d312 两案例（见正文变体）"
---

## 变体（2026-09-17 补，D-312/D-301c 施工实证）：`;` 顺序链 + pull --rebase 冲突

管道不是唯一吞码方式：`cmd1; cmd2` 顺序链不消费退出码——`git pull --rebase` 撞 STATE.md
冲突（exit=1）后，同一命令串里的 `git push` 照常执行，把 **rebase 前的旧基 tip** 推上远端
（本地 HEAD 停在 rebase 中间态；`git rev-parse HEAD` ≠ `git ls-remote` 两个哈希对不上即暴露）。

## 处置

- ①commit/rebase 后不得接管道或 `;` 链续接 push——push 单独一条命令发，或 `set -o pipefail` + `&&` 链；
- ②push 前后各验证 `git rev-parse HEAD` vs `git ls-remote`（双锚相等才算推上）；
- ③`git pull --rebase` 冲突=停手回报，解冲突走「对账定源→合并保留双方语义→rebase --continue」；
- ④纠正已推错的分支用 `git push --force-with-lease=<已知远端旧hash>`，禁裸 `-f`。
- 两案例均以上述流程闭环：D-312（03568a15 基→6d0b9f9d 收编）、D-301c（97dd2562 基→c60d0e57 收编）。

## 变体（2026-09-22 补，Claude 席实证）：merge|tail 掩码 → push 推了**别人的** hash

`git merge --ff-only 6263b6ae 2>&1 | tail -2 && git push origin master:master`——merge 因
对方（Devin）并发推进 master 而失败，管道退出码取 tail=0，push 照常执行。推上去的范围
是 `9d6a44ad..cefab8d7`（cefab8d7 是对方刚完成的合并，非我方载荷）——**本意交付的
6263b6ae 静默未上 master**。与前两变体的差别：不是推空分支，是推了**内容正确但归属
错误的 ref**（部署树被他席并发推进时，push 的 local master 已不是你 merge 前的那个）。
救命的是既有纪律②：push 输出 range 尾 hash 不是自己的提交即暴露。教训追加：
ff 合并后立即 push 的链路里，merge 与 push 之间**不允许有任何管道**；且 push 输出的
`old..new` 尾锚必须等于你刚 merge 的 hash，不等即停手对账。
