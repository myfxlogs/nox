---
schema_version: 2
id: timer-last-resort-event-first
kind: pattern
risk: medium
status: verified
keywords: [定时器, 轮询, 事件驱动, cron, 状态盲, boot 协议, 断流兜底, 合并节流]
scope: [autotrade, dashboard, web, ops]
summary: 定时器最后手段——新功能先找事件源（网关流/总线/SSE/用户动作/boot）；找不到时先挂「必然发生的生活事件」（boot 协议）；仍无则低频兜底定时器（有界+事件不可靠辩护）；定时任务=静态快照状态盲，真相只存 boot 时读的持久文档
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: docs/D-107事件驱动二阶段-施工提示词.md（全量审计清单+四期替换）
  - type: server_fact
    source: 会话 cron（b2170503/d8addcfd）
    query: #264 平仓后 cron 提示词仍查持仓（状态盲实证）
    observed_at: 2026-08-25
---

## 事实/症状

两个麻烦实例：① 会话 cron 为「8-27 三倍终判」创建，提示词冻入「查 #264 持仓」——#264 平仓后任务不自知，仍排队执行过期指令（状态盲）；② 设计文档随手写「30s 轮询刷新」，被业主拦下。全量审计（D-107）后分类：可事件化 7 项、合理保留 12 项、已事件化 4 项，全部替换落地。

## 根因或策略解释

- **定时任务=静态快照**：创建时把当时假设冻进提示词，前提状态变了任务不自知；且与持久文档一事实两处（P3）。
- **替换优先级链**（业主判据）：① 上游事件源（网关流/总线/SSE/用户动作）→ ② 「必然发生的生活事件」（每天开工=boot 协议，判据写 git 文档随状态更新）→ ③ 低频兜底定时器（须满足：上游无事件源 **且** 每 tick 有界；事件源不可靠时可保留有界兜底）→ ④ 才是一般定时器。
- 轮询降噪技巧：**断流期间才轮询**（流健康零轮询，断流时 30s 对账兜底）；事件触发的「合并防抖」（buffered(1) chan 非阻塞 send / setTimeout 合流）不是轮询。

## 处置

- 新组件设计第一步：问「什么事件触发它」；找不到事件源 → 问「什么必然发生的生活事件可以挂」；再不行才写定时器并附辩护。
- 既有轮询的辩护格式：上游无事件源（可实证）+ 每 tick 有界（可测算）+ 兜底语义。

## 证据

- check: D-107 全期落地——前端 4 处轮询消灭（case 11 事件+断流兜底门控）、bpMon/accMon/engine 事件化、定时器只剩 12 项有辩护保留
- result: 会话 cron 删除（判据移 STATE 下一步①，boot 协议覆盖）
- ref: 会话 2026-08-25（D-107 立项到闭环）
- check: D-114 续篇——D-107 P3 "流健康时零轮询"只改轮询逻辑未审计 alerts/positions 事件源覆盖→snap.alerts/positions 永不更新（假事件驱动）；修复=InsertAlert 加 alertHook→alertTopic→case 3 + case 11→load() 补齐
- lesson: **事件驱动迁移须审计每个数据源是否有事件推送路径**——删轮询≠事件驱动，须确认每个 snap 字段都有对应 case 推送或事件触发 load()

## 反证条件

- check: 新增组件再出现无辩护定时器
- falsifies_when: 出现「有事件源却用定时器」且无性能/可靠性辩护

## 适用边界

- 一切周期性任务与 UI 刷新；监控采样（健康检查）本身无事件源=合理保留项。
- 相关：[[owner-prefers-event-driven]]（私有记忆）、[[construction-claim-needs-machine-check]]。
