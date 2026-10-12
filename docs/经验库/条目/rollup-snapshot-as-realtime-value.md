---
schema_version: 2
id: rollup-snapshot-as-realtime-value
kind: pitfall
risk: high
status: verified
keywords: [rollup, combo_daily_rollup, current_net_bps, 实时, 快照, 事件驱动, 数据源选择, ListComboDaily, ORDER BY ASC, limit=1]
scope: [dashboard, service_positions_enrich, convergentTypes, CROSS_VENUE, MEANREVERT, QuoteBus, D-080, D-111]
summary: 用事件驱动的日度统计聚合（combo_daily_rollup）充当查询时的实时值（current_net_bps）——rollup 只在机会事件流入时更新，combo 无新事件时"实时"永远等于"预期"；实时展示必须用实时数据源（QuoteBus）查询时重算
first_seen: 2026-08-26
last_verified: 2026-09-26
skills: [事实核验, KB维护]
evidence_refs:
  - type: server_fact
    source: postgres
    note: "combo_daily_rollup WHERE combo_key='CROSS_VENUE:ETHUSDT:277842155:mt5:REDACTED_MT_ACCT:mt5' AND day='2026-08-26' → 0 rows（#294 当日无新样本）"
  - type: code
    source: internal/dashboard/service_positions_enrich.go:88-97
    note: "收敛型 current_net_bps 从 ListComboDaily(comboKey, 1) 取 net_avg"
  - type: code
    source: internal/store/pgstore/combo_daily.go:66
    note: "ORDER BY day ASC + limit=1 = 取最早一天（应为 DESC）"
---

## 症状

收敛型（CROSS_VENUE/MEANREVERT）持仓的"实时"净 bps（`current_net_bps`）永远等于"预期"净 bps（`expected_net_bps`），不随市场价差变化更新。前端显示"实时"列是死值。

## 根因

`current_net_bps` 从 `combo_daily_rollup` 表取 `net_avg`，但 rollup 是**事件驱动的日度统计聚合**（D-080 S4 对账报表用），只在机会事件流入时 Record+Flush。如果某 combo 长期无机会事件流入（如某品种跨 venue 报价不活跃），rollup 永远是旧值。

**本质**：用事件驱动的统计聚合快照充当查询时的实时值——数据源选择错误。rollup 回答的是"今天这个 combo 的机会事件平均 bps 是多少"，不是"当前市场价差是多少"。

## 实例

#294（CROSS_VENUE ETHUSDT 277842155:mt5 vs REDACTED_MT_ACCT:mt5）开仓于 8-25，`expected_net_bps=42.3`。该 combo 8-26 无新机会事件流入（ETHUSDT 跨 venue 报价不活跃），rollup 只有 8-25 的 1 条样本（42.3）。`current_net_bps` 回显了开仓时的预期值，"实时"等于"预期"。

附带 bug：`ListComboDaily` 用 `ORDER BY day ASC`（对账报表设计），但 `enrichPositions` 用 `limit=1` 调用时 ASC+limit=1 取的是**最早的一天**，进一步加重了"取旧值"问题。

## 修复

`current_net_bps` 改用 QuoteBus 实时报价重算（D-111）：
- CROSS_VENUE: `net_bps = (sell.Bid - buy.Ask) / mid * 10000 - (commissionBps + slippageCost) * 2`
- MEANREVERT: `bps = spread / ask * 10000`（gross）
- 报价缺失 → `current_net_bps` 缺省（fail-closed，不回退 rollup）
- ListComboDaily 排序 ASC→DESC（附带修复）

## 教训

1. **数据源匹配语义**：实时展示用实时数据源（QuoteBus 缓存），统计报表用统计数据源（rollup 聚合）。不要用统计聚合充当实时值。
2. **事件驱动 vs 查询时计算**：事件驱动的累积器（accumulator）只在事件流入时更新。如果展示需要"当前值"，必须在查询时用当前数据源重算，不能依赖事件驱动的快照。
3. **排序方向 + limit=1 陷阱**：ASC + limit=1 = 取最早一行；DESC + limit=1 = 取最新一行。调用方必须确认排序方向与取值意图一致。
4. **"实时"必须有数据源保证**：如果"实时"字段的数据源不是实时更新的，那"实时"就是误导命名。
