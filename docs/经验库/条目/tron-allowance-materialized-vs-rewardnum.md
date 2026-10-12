---
schema_version: 2
id: tron-allowance-materialized-vs-rewardnum
kind: pitfall
risk: high
status: verified
keywords: [TRON, getaccount.allowance, rewardNum, 物化, 应计, 差分计量, tronscan, 回退0]
scope: [TRON 投票奖励的展示/日结计量/差分链路]
summary: getaccount.allowance 是 tx 事件物化值（投票/领奖才更新，claim 后长期归零不回填），不是实时应计；任何差分/日结计量必须用 tronscan rewardNum（应计真值），且 tronscan 失败禁写 0（回退上一快照，否则污染差分+展示闪 0）。
first_seen: 2026-09-13
last_verified: 2026-09-13
repeat: 1
skills: [事实核验, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-295
  - type: decision
    ref: D-285
---

## 事实/症状

D-295 审计实查（arb 生产库）：9-12 20:25 `vote` tx 才把 allowance 从 0 物化到 12.05M（9-11 起 ~30h 应计整包）；9-13 00:30 自动领奖后 allowance 归零 7h 平 0，而 tronscan rewardNum 已回升 2.4M；同窗口 1359 行快照中 17 行 tronscan 失败被写 rewardNum=0。后果：rollup 用 Δallowance 计量投票日收益 → 领奖日必记 0、物化日记整包（跨日错位）、claim 边界应计永久漏计。

## 根因或策略解释

TRON 链上 `allowance` 字段只在账户发生交易（投票/领奖等）时物化结算进账户存储，两次交易之间不随应计增长；tronscan `rewardNum` 是链上实时计算的应计值。两者语义不同：前者=「已物化未领取」，后者=「真实应计」。经验库 trongrid-405 条目断言「未领投票奖励=getaccount.allowance」仅在展示瞬时值场景近似成立，计量场景不成立。另：外部 API 失败回退写 0 与合法归零（claim 后真 0）不可区分，0 是差分链路最毒的哨兵值。

## 处置

- 展示与计量一律用 rewardNum（D-285 展示层已切，D-295 修度量层：Δ 源 AllowanceSun→RewardNumSun，claim 归零被 Σmax(0,Δ) 天然滤掉）。
- tronscan 失败回退上一快照值（LastTRXAccount().RewardNumSun），首行无前值才允许 0。
- 任何「外部源失败回退默认值」的字段，先问默认值是否与合法状态值撞车。

## 证据

- check: fin_trx_account 9-12 13:05→9-13 00:29 窗口 17/1359 行 reward_num_sun=0（tronscan 失败写 0 实证）
- check: fin_trx_txs 9-12 20:25 vote tx / 9-13 00:30 vote_claim 14.45M 与 allowance 物化/归零时刻互证
- check: 9-13 rollup（allowance 差分口径）预演 voting=0 vs rewardNum 差分口径 ~2.4 TRX

## 反证条件

- check: 若 TRON 节点改为实时物化 allowance（或 gotron-sdk 提供应计查询）
- falsifies_when: allowance 与 rewardNum 在无交易时段同步增长（本条计量部分退役，回退 0 部分仍有效）

## 适用边界

一切 TRON 投票奖励的展示/日结/差分用途；跨项目可晋升（TRON 域通用，与具体项目无关）。
