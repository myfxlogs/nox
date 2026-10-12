---
schema_version: 2
id: unclaimed-parallel-seat-implementation
kind: pitfall
risk: high
status: verified
keywords: [认领制, 双头施工, 并行席位, 竞合, worktree, 在飞代码, 镜像污染, 未提交代码, 同一任务单一owner, 产出即提交, STATE施工表, D-246, D-220, D-243]
scope: [workflow, 多席位协作, 部署链, 施工表]
summary: 同一 SSOT 双席并行施工且一方未走认领制=必产双真相源——竞合评审收编一套作废一套（败者整票工时归零），且未认领在飞代码可从共享 worktree/镜像构建链漏进生产（实证 0 实害属侥幸）；认领制空转是竞合浪费与污染事故的共同根
first_seen: 2026-09-25
last_verified: 2026-09-26
skills: [KB维护, 验证与交接, 生产取证]
evidence_refs:
  - type: decision
    ref: D-246
  - type: decision
    ref: D-220
  - type: decision
    ref: D-243
  - type: commit
    ref: 5f6350d5
  - type: commit
    ref: c65501b9
  - type: commit
    ref: 6cf00a4c
  - type: commit
    ref: c9e54d59
  - type: doc
    ref: docs/handoff/LOG.md 「2026-09-25 15:33Z 事故记录」「2026-09-25 D-491 双实现竞合裁定」
---

## 事实/症状

D-491（PERP_FUNDING 证据准入）同一 SSOT 出现两套独立实现：

- A=ZCode 席 seat/zcode-d491 正式认领施工（`5f6350d5`，约 2.5k 行）
- B=某 Devin 终端在 seat/devin-d486 树内**未认领**塞进更深一套（`c65501b9`，约 3k 行，migration+评估器+service+前端+测试五件套齐）

两条同根后果：

1. **竞合浪费**：决策席逐套对锚评审后收编 B、作废 A——A 席整票工时归零（技术质量不是根因，重复开工才是）。
2. **污染链**：未认领侧在飞代码一度混入生产镜像（23:14 部署 binary 含未提交文案「证据评估 denied/pending」，部署树源码为旧版）——`c9e54d59` 事故记录实证，no-cache 重建止血，污染窗口 0 机会/0 决策/0 开仓系侥幸非机制。

## 根因或策略解释

认领制三条联动任一缺位即空转：

- 开工不挂 STATE 施工表 owner 行 → 其他席位从 SSOT 层看不见该任务有人在做；
- 席位 worktree 对在飞代码是黑盒（P1 通道不变量）→ 未提交即不存在，但共享部署树的构建上下文和镜像 tag 又把它当真代码吃进；
- 同一 SSOT 无 owner 锁 → 双席各自完整实现必然发散成双真相源，缝合无解只能二选一作废。

本质：认领制不是流程装饰，是「在飞代码唯一可见性通道」+「任务互斥锁」的合一体——空转时竞合与污染是同一枚硬币两面。

## 处置

**预防（开工侧，机械三步）**：

1. 开工即认领：STATE 施工表目标行先写 owner 席位，再动手——该行已有 owner=停手回决策层（D-246 同一任务单一 owner）。
2. 首个可交付单位立即 commit+push 到席位分支（D-220 v2 产出即提交）——未推送的在飞代码对其他席与远端不存在。
3. 施工只在席位 worktree（D-243）；部署树禁施工、禁留未提交漂移。

**竞合已发生时的处置流程（决策席）**：

4. 各自 commit+push 报审后，决策席逐套对 SSOT 对锚、各自机检独立重跑、出 disqualify 清单；
5. 定基座整包收编，败者作废——**禁 diff 缝合**（两套独立实现缝合=双真相源入仓）；
6. 部署前核验 binary↔HEAD 特征串一致性（grep 新功能文案/旧文案对照），防在飞代码混入镜像。

## 证据

- check: `git log --all --oneline --grep "D-491"` + `strings binary | grep 证据评估`
- result: 双实现双 commit 并存（5f6350d5/c65501b9）；生产 binary 含未提交文案而部署树源码无→污染实锤
- ref: 事故记录与竞合裁定均滚入 `docs/handoff/LOG.md`（2026-09-25/26 段）；B 收编 merge `6cf00a4c`

## 反证条件

- check: STATE 认领行+首 commit 即推纪律全执行后，观察是否仍发生「不可见在飞代码进生产」或「同任务双实现」
- falsifies_when: 认领制如实执行下仍发生上述事件 → 说明根因不止认领空转（如构建链存在其他未隔离入口），本条根因需修订；若席位 worktree 在飞面引入全局可见机制，条目转 invariant。

## 适用边界

多席位协作+共享部署树/共享镜像 tag 模型下成立；纯单席开发场景竞合面不适用，但「产出即提交」与「部署前 binary↔HEAD 核验」两条仍独立成立。
