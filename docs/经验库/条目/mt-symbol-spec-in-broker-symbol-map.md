---
schema_version: 2
id: mt-symbol-spec-in-broker-symbol-map
kind: ops
risk: low
status: verified
keywords: [contract_size, broker_symbol_map, mt_symbol_meta, MT5, 名义换算, USOILm]
scope: [MT5 腿, 名义换算, broker_symbol_map, facts]
summary: MT 合约乘数权威源是 broker_symbol_map 不是 facts——mt_symbol_meta 只采 calc_mode，查 facts 会误判「规格未采」
first_seen: 2026-09-17
last_verified: 2026-09-17
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-320
  - type: doc
    ref: internal/dashboard/service_legs.go:125
  - type: server_fact
    source: postgres
    query: SELECT venue, broker_symbol, canonical, contract_size FROM broker_symbol_map WHERE broker_symbol='WTI'
    observed_at: 2026-09-17T06:10:00Z
---

## 事实/症状

D-320 可行性扫描时在 facts 表查 `mt_symbol_meta contract_size` 得零行，一度记为「合约规格未采缺口」——实际 `broker_symbol_map` 早有权威数据（WTI→USOILm、contract_size=1000，两账户齐备）。

## 根因或策略解释

fact 采集链的 `mt_symbol_meta` 只落 calc_mode 等子集，contract_size 走的是 `broker_symbol_map` 静态映射表（D-042 backfill）。执行链路 `legContractSizeOf` 本就查 map 表——「执行依赖的规格」与「fact 采集的元数据」是两份存储，查错地方会得到假的「数据缺失」结论。

## 处置

- 查 MT 合约规格/符号映射：`broker_symbol_map`（contract_size、canonical↔broker_symbol）为权威；facts 只作补充
- 同类对照：CEX perp 合约面值在 `fin_cex_universe.ct_val`（gate=quanto_multiplier、okx=ctVal、binance=1.0），也不在 facts
- 「未取证」判定前先枚举该域全部候选表（D-224 语料纪律的表级应用）

## 证据

- `SELECT ... FROM broker_symbol_map WHERE broker_symbol='WTI'` → 277842155/3001157 USOILm contract_size=1000（pos 485 执行链同源取值实证）
- `facts mt_symbol_meta` 同 venue 仅 calc_mode/product，无 contract_size 行
