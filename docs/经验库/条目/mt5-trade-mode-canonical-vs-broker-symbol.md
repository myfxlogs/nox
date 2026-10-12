---
schema_version: 2
id: mt5-trade-mode-canonical-vs-broker-symbol
kind: pitfall
risk: high
status: observed
keywords: [MT5, trade_mode, ONLY_CLOSE_POSITION, canonical, broker symbol, AUDJPYm, 执行预检, facts 盲区]
scope: [MT5 适配层, 执行预检, swap 套利, 实盘测试, Exness]
summary: MT5 trade_mode facts 按 canonical 存（AUDJPY=Full），但 broker 实际 symbol（AUDJPYm）可能是 CloseOnly——执行预检放行但 broker 实际拒绝，造成单腿裸露（LEGGED）。
first_seen: 2026-08-31
last_verified: 2026-08-31
skills: [KB维护, 事实核验]
evidence_refs:
  - type: commit
    ref: ac6f39b
  - type: doc
    ref: internal/fact/fact.go:41
  - type: server_fact
    source: postgres
    query: SELECT DISTINCT venue, symbol, value FROM facts WHERE venue='59892915' AND kind='trade_mode' AND symbol LIKE 'AUDJPY%'
    observed_at: 2026-08-31T04:45:00Z
    server_revision: ""
    response_hash: ""
  - type: server_fact
    source: mt5
    query: posID=468 EXR leg ordersend error
    observed_at: 2026-08-31T04:44:53Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

posID=468 实盘测试：ICR(REDACTED_MT_ACCT) BUY AUDJPY 0.01 成交，EXR(59892915) SELL AUDJPYm 0.01 被拒：
```
[exec] leg 59892915:mt5 SELL AUDJPYm: mt5 ordersend: [code=ONLY_CLOSE_POSITION]
The request is rejected, because the "Only position closing is allowed" rule is set for the symbol (:59892915)
```

但 facts 表 EXR AUDJPY trade_mode=4(Full)：
```sql
SELECT DISTINCT venue, symbol, value FROM facts WHERE venue='59892915' AND kind='trade_mode' AND symbol LIKE 'AUDJPY%';
-- 59892915 | AUDJPY | 4 (Full)
```

执行预检 checkTradability（service_legs.go）查 canonical AUDJPY trade_mode=Full → 放行 SELL 腿，但 broker 实际 symbol AUDJPYm 是 CloseOnly → broker 拒绝 → 单腿裸露（LEGGED）。

## 根因或策略解释

MT5 ENUM_SYMBOL_TRADE_MODE 按 broker 原始 symbol（AUDJPYm）设置，但 facts 表按 canonical（AUDJPY）存储 trade_mode。canonical 映射时 trade_mode 取了 canonical 的值（Full），而非 broker 实际 symbol 的值（CloseOnly）。

**Exness 平台对某些 symbol 在特定账户类型/时间/区域下做 CloseOnly 限制**——这是平台级营销/风控策略（如 Exness 0 swap 营销策略可能伴随 CloseOnly 限制），非账户配置问题。facts 表的 trade_mode 是采集时的一次快照，可能：
1. 采集时该 symbol 是 Full，之后平台改为 CloseOnly（动态限制）
2. canonical 映射时丢失了 broker symbol 级的 trade_mode 差异

## 处置

1. **执行预检需查 broker 实际 symbol 的 trade_mode**：checkTradability 应查 venue_symbol（AUDJPYm）而非 canonical（AUDJPY）的 trade_mode。当前代码查 canonical，需修复为查 broker symbol。
2. **LEGGED 兜底**：单腿被拒后立即平掉已成交腿（posID=468 已用 ClosePosition RPC 平 ICR 腿闭环）。
3. **实盘测试前先手动 RPC 试单**：小额实盘测试前，先在目标 broker 手动开 0.01 手测试 symbol 是否可交易，避免系统自动开仓后单腿裸露。

## 证据

- check: `docker logs arb-core 2>&1 | grep 468`
- result: `pipeline_legs.go:56: [exec] leg 59892915:mt5 SELL AUDJPYm: mt5 ordersend: [code=ONLY_CLOSE_POSITION]`
- ref: commit ac6f39b（posID=468 实盘测试 ICR×EXR 阻塞+切回 demo）
- check: `SELECT DISTINCT venue, symbol, value FROM facts WHERE venue='59892915' AND kind='trade_mode' AND symbol LIKE 'AUDJPY%'`
- result: `59892915 | AUDJPY | 4 (Full)` ← facts 说 Full，broker 实际 CloseOnly

## 反证条件

- check: 查 EXR(59892915) AUDJPYm 的实际 MT5 ENUM_SYMBOL_TRADE_MODE（通过 MT5 SymInfo RPC 查 broker 原始 symbol）
- falsifies_when: 如果 AUDJPYm 实际 trade_mode=Full(4)，则根因不是 canonical vs broker symbol 差异，而是其他原因（如下单方向/账户类型限制）

## 适用边界

- 适用于 MT5 broker symbol 与 canonical 不同名的情况（如 AUDJPYm vs AUDJPY）
- Exness 平台 CloseOnly 限制可能动态变化（采集时 Full → 下单时 CloseOnly）
- 不适用于 broker symbol 与 canonical 同名的情况（如 ICMarkets AUDJPY=AUDJPY）
