---
schema_version: 2
id: test-fail-attribution-worktree-baseline
kind: pitfall
risk: medium
status: verified
keywords: [测试失败, 归因, worktree, 基线复现, 测试残留, 数据污染, 复审, 退回判定, 连跑失败, 测试库]
scope: [exec, pgstore, DSN 集成测试, 复审流程]
summary: 复审机检发现测试 FAIL 时，先重建干净库单跑受影响包，再用 git worktree checkout 变更前基线同参数复跑——基线同样红=既有/环境问题（挂账不阻塞本次），基线绿=本次引入（退回）；直接按「最后改的人负责」会把测试卫生债错算到施工包头上
first_seen: 2026-08-31
last_verified: 2026-08-31
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: code
    ref: internal/store/pgstore/positions_d106_test.go（TestCountPositions_ACTIVE 期望 4）
  - type: decision
    ref: STATE D-159 行挂账 W4a②（exec DSN 测试残留 positions 污染 pgstore 连跑）
  - type: commit
    ref: c5129f4（W3a 复审通过+挂账记录）
---

## 事实/症状

exec 包 DSN 集成测试跑完留 8 行 positions（id 60-67，5 个 OPEN）→ 与 pgstore 包连跑时 TestCountPositions_ACTIVE `want 4, got 5` FAIL、TestListPositions_OrderByClosedAtDesc `want 4 rows, got 5` FAIL；单包隔离跑全绿（依赖库初始状态），施工方自报「DSN 零 SKIP 全绿」与复审连跑结果矛盾。

## 根因或策略解释

测试间共享同一物理测试库且 A 包测试无收尾清理，B 包测试假设干净库——失败暴露的是**组合顺序依赖**，与被复审的代码变更无关。此时退回施工方=错误归因；放行不挂账=债隐形。

## 处置

归因三步：①重建干净测试库单跑受影响包（绿=代码对，脏=环境）；②完整组合复跑确认污染可复现；③`git worktree add <tmp> <变更前 commit>` 同参数跑——基线同样 FAIL 判既有（挂账+修法=收尾 `TRUNCATE positions CASCADE`），基线绿判本次引入（退回）。worktree 必须 `--force remove` 收尾；勿在主仓 checkout 旧提交（吞未提交修复风险见 reverse-proof-checkout-clobbers-uncommitted-fix）。

## 证据

- check: 2026-08-31 W3a 复审：arb_test 干净库 pgstore 单包绿；exec→pgstore 连跑 FAIL；worktree 基线 d6023b2 同样 FAIL → 判既有挂账 W4a，W3a 不受阻
- ref: 施工方 commit message 声称 DSN 全绿与连跑矛盾——单包绿≠组合绿，复审机检须按发布真实顺序连跑

## 反证条件

- check: 若测试基建改为每包独立 schema/库（隔离强制）
- falsifies_when: 连跑顺序不再影响结果（本条退役）
