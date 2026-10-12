---
schema_version: 2
id: reverse-proof-checkout-clobbers-uncommitted-fix
kind: pitfall
risk: medium
status: verified
keywords: [git checkout, 反证验收, 返修, 未提交改动, 覆盖, reverse-proof, 施工方]
scope: [internal/exec, 施工方返修流程, git 工作区]
summary: 反证验收临时改坏代码后用 git checkout -- 恢复，会把同文件里自己未提交的修复一并回滚——返修施工中同文件既有修复又有反证目标时禁用 checkout 恢复，用 cp 备份或精确 Edit 恢复
first_seen: 2026-08-30
last_verified: 2026-08-30
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: commit
    ref: 069f970
  - type: doc
    ref: internal/exec/pipeline_legs_flatten.go:101
---

## 事实/症状

D-159 W2a 返修（F1 测试+F2 一行 OrderID 修复，均未提交）做 A2 反证验收：sed 把 F2 修复行临时改回 `fill.OrderID` 跑测试确认红，随后 `git checkout -- internal/exec/pipeline_legs_flatten.go` 恢复——checkout 把文件恢复到 HEAD，**自己刚做的 F2 修复（同文件未提交改动）也被一并吞掉**，若不复查会以「F2 未修」状态提交 F1。

## 根因或策略解释

`git checkout -- <file>` 的语义是「丢弃该文件全部未提交改动回到 HEAD」，不区分「反证临时改动」和「本包正式修复」。反证验收恰好要求临时引入缺陷再恢复，与返修场景（同文件已有未提交修复）叠加时，恢复动作的作用域大于意图。

## 处置

- 提交前 `grep` 复核关键修复行仍在（本次实测抓回：`OrderID:` 仍为 `fill.OrderID` → 重新应用 F2）。
- 流程修正：反证验收前 `cp <file> /tmp/<file>.keep`，反证后 `cp` 回来；或用精确 Edit 反向替换而非 checkout。`git checkout --` 只适用于「该文件无其他未提交改动」的场合。

## 证据

- check: `git checkout --` 后 `grep -n "OrderID:" internal/exec/pipeline_legs_flatten.go` → 101 行仍为 `fill.OrderID`（F2 修复丢失）
- result: 重新 Edit 应用 F2 → 复跑双测试绿 → commit 069f970 diff 含 F2 修复行
- ref: commit 069f970

## 反证条件

- check: 在同文件「无未提交修复」的文件上做反证+checkout 恢复
- falsifies_when: checkout 仅回滚反证改动、保留同文件其他未提交改动（与 git 语义矛盾，不可能）

## 适用边界

- 适用于一切「临时改坏→验证红→恢复」流程（反证验收、先红后绿 TDD）；多 agent 同仓下即使改动属自己也未提交时均受限。
- 不适用于已提交基线的恢复（`git checkout <commit> -- <file>` 显式带 commit 时按 commit 语义）。
