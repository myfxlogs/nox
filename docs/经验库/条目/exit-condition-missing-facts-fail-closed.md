---
schema_version: 2
id: exit-condition-missing-facts-fail-closed
kind: pitfall
risk: high
status: verified
keywords: [退出条件, fail-closed, 缺数据, 假信号, 误平仓, facts 缺失, 静默取零, recalcNetSwap]
scope: [autotrade, engine, facts]
summary: 退出条件按 venue 匹配 fact 时「匹配不到静默取 0」= 缺数据被当收益死亡——测试截断 facts 表 → #264 被假「净 swap 归零」信号误平仓；缺数据必须 fail-closed（不决策）
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/autotrade/engine_position.go（recalcNetSwap/NetFunding D-105 T1 修复前形态）
  - type: server_fact
    source: 生产 positions 表 id=264
    query: closed_at=2026-08-25 04:29:15（真实成交，误平）
    observed_at: 2026-08-25
---

## 事实/症状

持有 4 天的 SWAPARB #264（净 swap +0.49 bps/日健康收租中、基差 -0.9 pips 持有区、距回本 2-3 天）被系统**自动平仓**——真实成交，但触发信号是假的：

- 施工方测试误截断生产 facts 表 → IC/Exness 的 swap facts 缺失
- `recalcNetSwap` 按 venue 匹配 fact：`for _, f := range longFacts { if f.Venue == buyVenue { swapLong = f.Value } }`——**匹配不到时 swapLong 保持零值**
- `0 + 0 = 0 ≤ SwapArbExitBps(0)` → 「净 swap 归零」→ 平仓

缺数据被当成了「收益源死亡」。修复（D-105 T1）：found 标志 + 匹配不到 → `log + return 0, false`（**不决策**）+ 新鲜度守卫改对匹配到的 fact。

## 根因或策略解释

- 退出/止损条件的默认方向必须 fail-closed：**数据不足 = 不决策**，而不是「取零值继续判」——零值在业务语义上恰好等于最激进的退出信号（净收益归零），静默取零 = 把最坏情况的决策权交给了缺失。
- 同族检查：凡按 key 匹配后直接用零值默认的聚合逻辑（netFunding、汇率、库存匹配）都有此病；修复时全部用 found 标志。

## 处置

- 所有依赖 facts/报价的退出条件：匹配不到 → 日志 + 不决策（return false）；匹配到再查新鲜度。
- 反例自查法：问「如果这条数据缺失，系统会做什么动作」——答案必须是「不动」，否则就是此坑。

## 证据

- check: #264 closed_at=04:29:15 UTC（与测试截断时刻 38 秒差）；净 swap 实况 +0.49>0；库存 realized=-2700（JPY 当 USD，另一坑）
- result: D-105 T1 修复 + #264 数据修正（-2700→-16.94）；误平不再可复现
- ref: 会话 2026-08-25（D-105）

## 反证条件

- check: 修复后再次清空某 venue facts 观察引擎行为
- falsifies_when: 缺数据仍触发平仓 → found 标志遗漏分支

## 适用边界

- 一切「聚合匹配→判阈值→执行动作」的自动化决策链；交易/风控/资金系统特高危。
- 相关：[[test-dsn-can-truncate-production]]（本次截断的起因）、[[unspecified-err-handling-defaults-unsafe]]。
