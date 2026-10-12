---
schema_version: 2
id: sizing-gate-must-use-margin-not-notional
kind: pitfall
risk: high
status: verified
keywords: [sizing, gate, leverage, margin, notional, 保证金, 杠杆, 名义价值, ratio, CFD, MT5]
scope: [autotrade, dashboard, SWAPARB, CFD, MT5, 杠杆账户]
summary: 杠杆账户 sizing gate 必须按保证金（名义/杠杆）而非名义价值来 gate；用名义价值 vs 权益×ratio 会误挡所有杠杆账户的最小手数开仓
first_seen: 2026-08-31
last_verified: 2026-08-31
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-174
  - type: commit
    ref: 7775128
  - type: doc
    ref: internal/dashboard/swap_notional.go:197
  - type: server_fact
    source: postgres
    query: SELECT venue, symbol, value FROM facts WHERE kind='init_margin_rate' AND venue IN ('REDACTED_MT_ACCT','59892915')
    observed_at: 2026-08-31T14:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

SWAPARB 自动开仓日志反复报 sizing 不足跳过，即使账户权益远超实际所需保证金：

```
SWAPARB skip GBPJPY REDACTED_MT_ACCT:mt5/59892915:mt5: sizing $250.00 < min lot notional $1354.40
SWAPARB skip SILVER 61585565:mt5/39050818:mt5: sizing $2498.68 < min lot notional $3324.75
```

业主质疑：「0.01 手需要 $1354？这些账号都是有杠杆的」——质疑成立。1:1000 杠杆下 0.01 手 GBPJPY 实际保证金 ~$1.35，但 gate 要求权益的 25%（$250）≥ 名义价值（$1354）。

## 根因或策略解释

sizing gate 用「权益×ratio vs 名义价值」比较，没考虑杠杆：

```go
sizing = minEq × ratio          // $1000 × 0.25 = $250
gate:  sizing < minNotional     // $250 < $1354 → skip（误挡）
```

`minNotional` = 0.01 手 × CS × price ÷ USDJPY = 最小手数的**名义价值**（非保证金）。gate 比的是「权益的 25%」vs「名义价值」——在杠杆账户下完全错误。

D-097 历史教训：当时已意识到杠杆问题（funding_need.go 注释「1:100 杠杆下 $1570 权益本可开 0.02 手，旧门误挡」），但修复方向错了——只加了 ratio 系数（从全款门改为权益×ratio），仍用名义价值 vs 权益比，没引入杠杆。

杠杆数据已采集（`fetchLeverage` → `init_margin_rate` fact → `AccountLeverage()`），但 sizing gate 没用它。

## 处置

ratio 语义从「名义/权益比」改为「保证金/权益比」：

```
旧：notional = minEq × ratio          → $1000 × 0.25 = $250 < $1354 → 阻塞
新：notional = minEq × ratio × leverage → $1000 × 0.05 × 1000 = $50000 > $1354 → 通过
```

三层 gate（sizing/bareCap/venue exposure）都引入杠杆：
- sizing: `maxNotional = minEq × ratio × leverage`
- bareCap: `bareNotionalLimit = minEq × bareCap × leverage`（保证金口径）
- venue exposure: `margin = exposure / leverage`（各腿自己杠杆）

杠杆从 `AccountLeverage(venueKey)` 读取（已采集），不用硬编码。无限杠杆哨兵（≥1e6）/未知（≤0）→ 保守回落 1.0。

.env ratio 需同步调整（语义变更）：旧 0.25（名义/权益）→ 新 0.05（保证金/权益，1:100 杠杆下名义=5倍权益，接近旧 0.25）。

## 证据

- check: `docker exec arb-postgres psql -U arb -d arb -c "SELECT venue, value FROM facts WHERE kind='init_margin_rate' AND venue IN ('REDACTED_MT_ACCT','59892915')"`
- result: REDACTED_MT_ACCT margin_rate=0.001（1:1000），59892915=0（Exness ∞ 哨兵）
- check: 部署后 `docker logs arb-core | grep 'sizing.*<.*min lot notional'`
- result: 零匹配（修复前每分钟多条）
- check: `docker logs arb-core | grep 'SILVER.*61585565.*39050818'`
- result: 通过 sizing gate，现被 gross exposure cap 拦截（`active $149930.97 >= $5000.00`）= 正常额度耗尽
- ref: D-174, commit 7775128, internal/dashboard/swap_notional.go:197

## 反证条件

- check: 在无杠杆账户（leverage=1.0）上跑 sizing gate
- falsifies_when: 无杠杆账户 sizing 行为与旧 D-097 R2 不一致（leverage=1.0 应等价旧行为，单测 4 旧 case 验证）

## 适用边界

- 适用于所有杠杆账户（CFD/MT5/MT4 + CEX perp）
- 无杠杆账户（leverage=1.0 回落）等价旧行为，无影响
- 杠杆未知/离线 → 保守回落 1.0（最严格，可能误挡但安全）
- gross exposure cap（D-168 绝对值上限）不受杠杆影响，独立于本修复
- ratio 语义变更是 breaking change，部署时必须同步调整 .env ratio 值
