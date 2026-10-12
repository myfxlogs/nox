---
schema_version: 2
id: cross-venue-data-borrowing-pollution
kind: pitfall
risk: high
status: verified
keywords: [跨venue污染, funding interval, swapChargeAt, 结算倒计时, venue原生, canonical共享键, OKX, Binance, 同名异期]
scope: [internal/dashboard, broker_position_build, cexStepMeta, funding_interval, CEX腿]
summary: 按 canonical 共享键跨 venue 借元数据（interval/step/结算时刻）=隐性污染——OKX 腿吃 binance 4h 期把 8h 结算倒计时算错；同类审计须按 venue×symbol 隔离，缺真值退 nil 诚实空态禁猜值兜底
first_seen: 2026-09-25
last_verified: 2026-09-25
skills: [事实核验, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-466
  - type: commit
    ref: 27fc20f0
  - type: doc
    ref: internal/dashboard/broker_position_build.go
  - type: doc
    ref: docs/D-466设计与施工提示词-腿级结算时刻venue原生读取-去跨venue污染-2026-09-24-Devin.md
---

## 事实/症状

`broker_position_build.go` 对 CEX 腿用 `cexIntervals.Lookup(canonical)` 单键查 funding 周期——缓存只装 binance fundingInfo 数据，但键是 canonical（XAUUSDT）不带 venue。生产实证：OKX `XAU-USDT-SWAP` 腿（posID=502，8h 结算）拿到 binance XAUUSDT 的 4h 期，持仓卡结算倒计时被算到 4h 边界，`periodicPct` 同时被 6 期/日腰斩（0.0158% vs 真值 0.0325%）。同 canonical 同名异期是常态（binance XAU 4h vs OKX 8h vs gate 4h）。

## 根因或策略解释

venue 元数据（结算周期/时刻/步长）是 **venue×symbol 归属** 的，canonical 只做跨 venue 配对键不做数据归属键。「同名=同参数」的隐式假设在跨 venue 消费侧不成立；更危险的是兜底值/借值会产出「看似合理但错」的数——业主令：禁猜，缺真值退 nil 诚实空态（前端渲「—」）。

## 处置

- 新 kind `funding_next_settle`/`funding_interval` fact 三所采集器同帧发出（各 venue 原生字段：binance `nextFundingTime`ms / gate `funding_next_apply`s+`funding_interval` / OKX `fundingTime`+`nextFundingTime` 差值）。
- 消费侧按 `(venue, canonical)` 双键取腿级真值；stale 闸=fact 龄 >2×周期即失效。
- 回退链：venue 原生 next_settle → venue 原生 interval 边界 → nil+warn；**跨 venue 缓存只在 venue 自洽时合法**（binance 腿用 binance 缓存可，OKX 腿不可）。
- 同类审计三处同查：income 边界（binance 采集器消费 binance 缓存=自洽不动）、stepMeta（binance 腿用 binance exchangeInfo=自洽）、`cexStepSizeFallback` 硬编码表（撤出腿级路径）。

## 证据

- check: T1 复现测 `TestD466_OKXLegVenueNativeSettle` 对旧 build 逻辑 → `swapChargeAt=binance 4h 边界`（红）
- result: 旧码产 20:00 4h 边界，OKX 真值应 8h 边界；修复后读 venue 原生 fact
- ref: commit 27fc20f0；`internal/dashboard/d466_settle_test.go`

## 反证条件

- check: 若某 venue 文档证明其周期/步长与另一 venue 强制同源（如托管镜像所）
- falsifies_when: venue 官方文档声明参数由上游所强制同步且运营实证无偏差

## 适用边界

- 适用一切「canonical/品种名做键、venue 数据源做单值表」的消费侧；MT5 侧 venue 实例本就分 key（`broker:mt4/5`）天然免疫。
- 白名单/配对键仍用 canonical——归属律只管数据读面，不管配对面。
