---
schema_version: 2
id: reset-display-vs-delete-data
kind: pitfall
risk: high
status: verified
keywords: [清零, 重置, 删除数据, 统计口径, anchor, 停机基准, 派生依赖, 过度执行, 展示层, 数据层]
scope: [ops, workflow, agent-collab]
summary: 用户说「把累计 X 清了」指展示数字非数据行——直接 DELETE 全表属过度执行，删数据副作用必须分析派生依赖；优雅解=统计起点 anchor
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: D-075 / 修复③ F4
---

## 事实/症状

业主要求「把累计亏损清了吧」——agent 执行 `DELETE FROM positions/position_legs/position_cashflows/strategy_stats`（全表交易历史），被权限门拦下。两个错误：① 过度执行——用户语言指向展示层语义（那个 -$100.90 的数字），不是数据层删除；② 副作用未预判——`DailyRealizedPnl`（日亏停机基准）从当日 CLOSED 行求和，删掉后 -$100.90「消失」→ 停机条件解除 → **引擎当天恢复开仓**（用户绝无此意）。

## 根因或策略解释

「清零/重置」类指令天然双义（口径重置 vs 数据删除）；数据行的派生依赖（风控基准/统计/外键）在删除前不可见。demo 数据「无所谓」的直觉会掩盖派生依赖伤害。

## 处置

① 破坏性操作前把指令翻译成两层问自己：用户指的是数字还是行？删这些行谁在依赖？② 重置类需求默认**统计起点 anchor**方案（展示口径 = `WHERE ts >= anchor`，重置=更新 anchor；数据真相完整、派生口径独立、可反复重置）；③ 硬删除仅当用户明确点名表+确认副作用后执行。

## 证据

- check: 权限拦截记录 + DailyRealizedPnl SQL（`SUM(realized_pnl) FROM positions WHERE status='CLOSED' AND closed_at>=今日`）
- result: 改 F4 anchor 方案（migration stats_anchor + ResetPnlAnchor RPC + 前端清零按钮），累计口径与日亏口径解耦
- ref: 修复③ F4

## 反证条件

- check: 后续「清零」类需求是否先出双义确认
- falsifies_when: 项目出现真实数据删除需求且用户明确确认 → 条目仍适用（确认流程），不失效

## 适用边界

所有统计展示 + 风控/派生基准场景。纯缓存/临时表清理不适用。
