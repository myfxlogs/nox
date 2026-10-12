---
schema_version: 2
id: subscribe-symbol-identity-mapping
kind: pitfall
risk: high
status: verified
keywords: [broker_symbol_map, identity, EURUSDm, cent, Subscribe, OnQuote, 零报价]
scope: [mt4, mt5, collect]
summary: DB 无映射写 identity 默认表 + cent 账号符号带 m 后缀 → 订阅静默零报价，适配器 ready 却失明
first_seen: 2026-08-18
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: commit
    ref: 8bdd95d
  - type: doc
    ref: internal/dashboard/broker_manager.go:814
---

## 事实/症状

`broker_symbol_map` 是**策略侧对照**（broker_symbol → canonical），`broker_symbol` 列只能填服务器真实符号。旧实现 DB 无映射时写 identity 默认表（`EURUSD→EURUSD`），把 canonical 当 broker_symbol；MT5/MT4 **cent 账号真实符号带 `m` 后缀**（`EURUSDm`），`EURUSD` 不存在 → mtapi Subscribe 静默成功但 OnQuote 零事件 → 适配器 ready 却 0 报价。

## 根因或策略解释

订阅符号必须来自服务器真实符号表，禁止凭空造。identity 默认表把 canonical 名当 broker_symbol 写入，cent 账号真实符号带 `m` 后缀，canonical 名在服务器不存在 → mtapi Subscribe 不报错但零事件。

## 处置

自愈防线（已在 code）：`symbolPlan` 启动时把映射的 broker_symbol 与服务器符号表比对，失效即删；全失效则按服务器符号自动解析默认表（精确 → `+m`）。**新增任何订阅路径都必须先过服务器符号校验**（P4 可检查）。

## 证据

- check: `grep -n "symbolPlan" internal/dashboard/broker_manager.go`
- result: 第 814 行 symbolPlan 定义，第 512/645 行调用点
- ref: commit 8bdd95d（自愈防线引入）

## 反证条件

- check: 审查 broker_symbol_map 无映射路径是否仍写 identity 默认表
- falsifies_when: broker_symbol_map 无映射路径不再写 identity 默认表（symbolPlan 语义变更）

## 适用边界

MT4/MT5 采集层，cent 账号场景。适用于所有通过 mtapi Subscribe 订阅报价的路径。
