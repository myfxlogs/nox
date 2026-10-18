---
schema_version: 2
id: aggregate-fingerprint-pooled-as-truth
kind: pitfall
risk: high
status: verified
keywords: [liveMap, MULTI, Σqty, 变更指纹, 粒度错配, venue:type:currency, fin_nav_daily, 腐败, binance earn, 覆写]
scope: [ledger, nav, budget, cex, 粒度错配族]
summary: 户级聚合指纹值（Currency=="MULTI" 的 Equity=Σqty 混合单位数）被按键 venue:type 分发到同户全部币种池——粒度错配致 fin_nav_daily 94 桶腐败两日、预算虚增 2.3×（被 maxNotional 帽掩未出血）
first_seen: 2026-10-17
last_verified: 2026-10-18
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: 'SG psql 2026-10-18：fin_nav_daily day_start 10-17/18 非 USD 桶 94 行——~85 桶 nav 雷同 101524.64（=binance:earn acct.Equity Σqty 指纹），USDT 桶 7063.97→108037.94；fin_strategy_budget 三 USDT 策略顶帽 4000（诚实 ~1766）'
  - type: code
    ref: 'internal/ledger/reconcile/cex_account_binance_earn.go:23——多币户 Currency="MULTI"、Equity=Σqty 自标「仅作变更指纹非真值」；cmd/core/main_monitors.go 旧 liveMapFn 键=venue:type（粒度断在户级）'
---

# 聚合指纹值被按池粒度分发消费（粒度错配族）

## 病灶

多币账户的户级 `Equity` 是**混合单位 Σqty 变更指纹**（DOGE 52886+USDT 1592+... 无单位可加——代码自标 `Currency=="MULTI"`、`Equity` 仅作快照变更检测指纹，真值在快照分币行）。消费侧映射键只到 `venue:type`（户级），把该指纹覆写到该户**每个币种池**——`venue:type:currency` 粒度的池各吃同一个户级幻影值。

实证链：binance:earn 85 资产池全被盖 101524.64 → fin_nav_daily 每桶 nav 雷同幻影、USDT 桶 7064→108037 → derive 预算顶帽 4000（虚增 2.3×）。USD 桶干净（mt 池免覆写）——**桶目同质雷同值=本坑签名**。

## 教训（粒度错配三审）

1. **聚合值进 map 前先问粒度**：任何 `venue→value` 映射若下游消费粒度到 `venue:ccy`，户级聚合值（尤其 MULTI/Σ 形态）就是注血源。键粒度必须与消费粒度对齐，不齐=禁入（fail-closed）。
2. **`Currency=="MULTI"`/Σ-fingerprint 是已声明的非真值标记**——消费侧遇之当 fail-closed 缺，禁当真值分发。账户级字段凡有「仅指纹/仅检测」自标者，建映射时显式跳过。
3. **修正径=分币源同取**：live map 分币键应取 `Holdings[].qty`（与快照分币行同源同单位），不是把户值拆开摊——拆摊仍是编数。

## 反签名（排查提示）

- 报表多行出现**同一个雷同大数**跨无关桶/资产=先怀疑聚合值被广播。
- 分币桶 nav 跳变日与某「实拉化/覆写」改动同日上线=粒度键嫌疑。

## 修复锚

- `cmd/core/live_equity_map.go` `liveEquityMapFromAccounts`：Holdings→`venue:type:ccy` 原生量键；无 Holdings→单币键；指纹值禁入（测试锚=指纹值全键扫描不存在）。
- 消费点 `budget.DailyCut`/`oppmatch.poolEquity`/`xferScan` 键全升三段粒度；映射缺=池剔除 fail-closed（P2③ 既定语义）。
- 迁移 1047 清 `fin_nav_daily` `currency<>'USD' AND day_start>='2026-10-17'` + `fin_strategy_budget` retired 策略化石行（retired 凭陈行可过预算闸=残额洞，删后 fail-closed）。

## 关联

- 修件合并 @nav-livemap（复审收口见 STATE 尾注）；同族：aggregate-fanout-into-rows（聚合扇出成行的对偶坑）。
