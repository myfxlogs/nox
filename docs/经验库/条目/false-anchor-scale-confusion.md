---
schema_version: 2
id: false-anchor-scale-confusion
kind: pitfall
risk: medium
status: verified
keywords: [对拍, 锚点, 口径, lot, scale, 伪观察, 幽灵偏差, 归一化, bps_daily, swap]
scope: [数据对拍, swap 归一化, 任何双源数值对照]
summary: 双源对拍时锚点自身 lot/量纲口径错误会制造「32-87x 系统性偏差」级幻觉，诱发设计绕路（D-126 因伪观察弃用 USD 门槛）。对拍前先统一双方量纲/lot scale，锚点用可复算的原始事实（快照/成交回读）。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-128
  - type: server_fact
    source: postgres
    query: SELECT venue, swap_usd FROM position_swap_snapshots WHERE position_id IN (290,291)
    observed_at: 2026-08-28T04:30:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

D-126 记录「swap_bps_daily 归一化与实际 swap_usd 存在 ~32-87 倍系统性偏差（估算 $0.047/day vs 实际 $4.08/day）」，据此弃用 USD 门槛改用 bps 门槛。D-128 复查：双腿对拍（591 腿 1.05x / 390 腿 1.02x）归一化实际正确——「实际 $4.08/day」锚点是错的。

## 根因或策略解释

对拍两源时只校验了公式侧，没校验锚点侧：$4.08 与 $0.047 相差 ~87x≈0.01 手 vs 1 手的名义比（lot scale 混淆）。错误锚点 + 正确公式 = 「系统性偏差」幻觉。

## 处置

对拍纪律：①锚点必须可从原始事实复算（position_swap_snapshots 日增量/成交回读），不用二手计算值；②对拍前先统一双方量纲（lot 数、计价币、单位）；③发现 N 倍级偏差先查两边口径，再怀疑公式。

## 证据

- check: facts 591 swap_long=+0.6486bps×$920=$0.0598/day vs 快照 ~$0.063/day（1.05x）；390 腿 1.02x
- ref: D-128（decisions.md）

## 反证条件

- check: 换手规模/其他品种再对拍
- falsifies_when: 多品种大样本出现一致 >2x 偏差（则归一化确有缺陷，本条退役）

## 适用边界

通用对拍纪律；本库场景为 swap 归一化，但「锚点口径错误制造幽灵偏差」适用于一切双源数值对照（预期 vs 实收、规格 vs 结算、文档 vs 服务器）。
