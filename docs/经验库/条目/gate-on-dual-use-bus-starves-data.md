---
schema_version: 2
id: gate-on-dual-use-bus-starves-data
kind: pitfall
risk: high
status: verified
keywords: [发布门, 双用途总线, pub/sub, rollup 断供, 无声事故, 注意力过滤]
scope: [engine, architecture]
summary: 双用途事件总线（推送面+数据管道）上加发布级别门=数据面静默断供且无任何报错；过滤只能放显示层，总线永不过滤
first_seen: 2026-08-23
last_verified: 2026-09-23
skills: []
evidence_refs:
  - type: server_fact
    ref: oppTopic 订阅方=autotrade oppLoop（rollup 喂入）+StreamUpdates；门开 20min rollup 零更新，撤线后 1min 恢复
    verified: 2026-08-23
  - type: decision
    ref: D-086 实施修正①（LOG 2026-08-23）
---

# 双用途总线上的发布门=数据面无声断供

## 症状
为「减少推送噪音」在事件 Topic 的发布侧加级别门（V0 不发）——前端看似正常（快照兜底），数据管道（rollup 聚合/执行触发）**静默断供，无错误无告警**；事后发现时样本缺口已永久无法补采。

## 根因
总线是双用途：注意力面（toast/卡片）+ 数据面（聚合/触发）。发布门作用于总线=两个面一起被切。且「要防的推送面」可能根本不存在（本案 toast 组件是无引用孤儿）——为一个不存在的问题切断真实的管道。

## 通用解法
- **过滤只在消费者/显示层**：徽章灰显、toast 侧按级别过滤；总线发布无条件。
- 加门前先枚举 `Topic.Subscribe()` 的全部订阅方并分类（注意力面 vs 数据面）——有任何数据面消费者即禁止发布侧过滤。
- 数据管道的断供要能被观测：聚合表 last_at 心跳检查（本案靠 rollup last_at 才实证断供与恢复）。

## 证据
- 2026-08-23 重现: 门开 08:37→rollup 零更新 20min→撤线 08:47→08:48 恢复 | ref: combo_daily_rollup last_at 时序
- 2026-08-23 重现: NewOpportunityToast 孤儿组件（无引用）=门防的推送面不存在 | ref: grep web/src
