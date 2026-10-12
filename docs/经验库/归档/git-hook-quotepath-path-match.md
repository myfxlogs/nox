---
schema_version: 2
id: git-hook-quotepath-path-match
kind: pitfall
risk: high
status: verified
keywords: [git, hook, core.quotepath, 中文路径, 转义, path match, grep, pre-commit, 静默跳过]
scope: [git hooks, 路径过滤, 任何按文件名分支的脚本]
summary: git hook 里用 `git diff --cached --name-only | grep '^中文/路径'` 做分支——core.quotepath 默认 true 把非 ASCII 路径转义成 `"\347\..."` 引号开头，锚定失配 → 分支静默跳过、门禁形同虚设。修法=`git -c core.quotepath=off`。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-130
  - type: doc
    ref: scripts/hooks/pre-commit
---

## 事实/症状

D-130 给 pre-commit 接 arbkb 门禁（涉及 docs/经验库/ 才触发）。中文路径提交实测：if 分支静默跳过、hook exit 0——门禁从未执行；负测试（故意塞坏条目）也放行。同一坏条目手动跑 `gen --check` 正确 exit 1。

## 根因或策略分析

`git diff --cached --name-only` 默认 `core.quotepath=true`：非 ASCII 路径输出为带双引号的八进制转义（`"docs/\347\273\217..."`）。`grep '^docs/经验库/'` 行首锚定对引号开头的行永不匹配 → 条件假 → 整段检查被跳过，且无任何报错（静默失败）。

## 处置

hook 内所有按路径分支的 git 列表命令统一加 `-c core.quotepath=off`。负测试必须做（塞坏输入验门禁真会拦），「门禁随 commit 通过」不等于「门禁执行过」。

## 证据

- check: 修复前坏条目 staged → hook exit 0（失效实证）；修复后同一坏条件 → FAIL exit 1；正常路径 → 通过
- ref: D-130 追加修复（2026-08-28）

## 反证条件

- check: git 新版默认改 quotepath=off
- falsifies_when: 中文路径默认原样输出（防御可留，条目降级）

## 适用边界

所有 git hooks / CI 脚本中「输出文件列表再 grep 过滤」的模式；纯 ASCII 仓库不触发但防御性加 `-c` 无害。
