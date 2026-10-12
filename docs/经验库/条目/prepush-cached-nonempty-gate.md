---
schema_version: 2
id: prepush-cached-nonempty-gate
kind: pitfall
risk: high
status: verified
keywords: [pre-push, hook, stdin, while read, 暂存区非空, 门禁零触发, git diff --cached, core.hooksPath, HEAD 推送]
scope: [git hooks, scripts/hooks, 多席并发仓]
summary: pre-push 内两段读 stdin 必须顶部一次缓存共用（while read 耗尽后段静默零触发）；HEAD:dst 推送时 local_ref=HEAD 非 refs/heads/*，ref 筛选须放行 HEAD 否则门禁被绕过
first_seen: 2026-09-22
last_verified: 2026-09-22
skills: [验证与交接]
evidence_refs:
  - type: decision
    ref: D-386
  - type: doc
    ref: docs/D-386设计与施工提示词-prepush暂存区非空门禁-2026-09-22-Claude.md
---

## 事实/症状

在既有 `while read` 消费 stdin 的 pre-push hook 尾部追加第二段检查，第二段读到空 → 按「stdin 空 → exit 0」语义**静默零触发**：门禁上线但永不拦截（比无门禁更危险，有拦截预期而无拦截）。

施工实证（本条目验证轮）：`git push remote HEAD:refs/heads/x` 时 git 送入 stdin 的 local_ref 字面为 `HEAD` 而非 `refs/heads/*`，按「非 refs/heads/* 跳过」筛选会把真实新提交推送判为无推送 → 脏态放行。

另：本仓 `core.hooksPath` 为绝对路径 `/opt/arb/scripts/hooks`，席位 worktree 验证新 hook 必须 `git -c core.hooksPath=<席位树>/scripts/hooks push`（且 hooksPath 是**目录**不是文件，指到文件=hook 不执行、测试空过）。

## 根因或策略解释

- stdin 是流，`while read` 循环读到 EOF 后后续读取恒空；git hook 的 stdin 行不会自动重放。
- local_ref 字段取命令行书写的源 ref 字面量，不做规范化；`HEAD:dst` 是常见推送写法。
- core.hooksPath 语义是目录前缀拼接 hook 名，误传文件路径时 git 静默找不到 hook，push 照常成功。

## 处置

1. hook 顶部 `stdin_buf="$(cat)"`，所有读段统一 `done <<< "$stdin_buf"`。
2. ref 筛选 case 放行 `refs/heads/*|HEAD`，再按「local_sha 全零=删除」「local_sha==remote_sha=无新提交」跳过。
3. 验证 hook 类改动时，先确认 hook 真的被执行（stderr 加标记或故意 exit 1 探针），再谈断言——hooksPath 指错/未 chmod/旧版残留都会让测试空过。

## 证据

- check: `/opt/arb` staged 脏 + `git -c core.hooksPath=<新hook目录> push <bare> HEAD:refs/heads/t8`
- result: exit 1，stderr 含 `[pre-push D-386]`（修复 HEAD 筛选后）；修复前同场景 exit 0 空过
- ref: D-386 验收锚 1b，2026-09-22

## 反证条件

- check: 故意 `exit 1` 探针 hook + 真实 push
- falsifies_when: 探针未触发却断言业务断言通过 → 该测试对 hook 无约束力

## 适用边界

git ≥2.40 本机（2.43.0 实证）；仅覆盖已 tracked 文件的 staged/worktree 改动，untracked 不在范围（D-386 §5 已知盲区）。
