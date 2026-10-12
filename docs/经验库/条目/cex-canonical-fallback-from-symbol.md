---
schema_version: 2
id: cex-canonical-fallback-from-symbol
kind: pitfall
risk: medium
status: verified
keywords: [CEX, perp, spot, canonical, symbol_mappings, qbus, 现价, currentPrice, 显示空]
scope: [broker position build, CEX 采集器, canonical 统一符号]
summary: CEX 持仓现价「—」——canonical 写入路径（采集器派生）与查询路径（DB symbol_mappings）不同构：CEX venue 无映射记录 → canonical 空 → qbus 查不到报价。修法=build 侧 CEX venueKey 从符号派生 canonical 同构 fallback。
first_seen: 2026-08-27
last_verified: 2026-08-28
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/dashboard/broker_position_build.go
  - type: server_fact
    source: rpc
    query: ListBrokerPositions #378 currentPrice
    observed_at: 2026-08-27T00:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

Broker 实持仓表中 CEX perp/spot 持仓的「现价」列显示「—」，但 `pnl` 等其他字段正常返回。MT5/CFD 持仓现价正常。

## 根因或策略解释

`broker_position_build.go` build 中 canonical 从 `symbol_mappings` 表查（`BrokerSymbolMapWithCS`）。MT5 broker 有映射记录（如 TRON→TRXUSDT），但 CEX perp/spot（binance/okx/gate）**无 symbol_mappings 记录** → canonical 空 → `qbus.Latest(canonical, venueKey)` 查不到报价 → `currentPrice=0, fresh=false` → 前端显示「—」。

CEX 采集器（okx_ws.go / exchange.go）用**派生 canonical** 写入 qbus（binance `TRXUSDT`→`TRXUSDT`；okx `TRX-USDT-SWAP` 去 `-SWAP` 替换 `-`→`TRXUSDT`），build 查 canonical 走 DB 映射表未走派生 → 采集与查询路径不一致。

## 处置

build 中 canonical 为空时对 CEX venueKey 从符号派生 canonical（与采集器同构）：`canonical == "" && isCEXVenueKey(venueKey)` → `cexCanonical(symbol)`（TrimSuffix "-SWAP" + ReplaceAll "-"）。

## 证据

- check: #378 binance:perp currentPrice=0.33876 fresh=true；okx:perp currentPrice=0.33881 fresh=true
- ref: docs/handoff/LOG.md 2026-08-27 收工清扫

## 反证条件

- check: CEX venue 有了 symbol_mappings 记录且 canonical ≠ 派生值
- falsifies_when: 派生 fallback 与 DB 映射冲突覆盖权威映射（当前实现 DB 优先、派生仅空时兜底，不冲突）

## 适用边界

canonical（统一符号）写入路径与查询路径必须同构。MT5 走 DB 映射（人工配置，canonical 可不同于符号）、CEX 走派生——两种路径并存，build 必须两条都覆盖。新增 venue 类型时同理检查。
