---
schema_version: 2
id: symbol-map-sql-insert-bypasses-activation
kind: pitfall
risk: high
status: verified
keywords: [broker_symbol_map, SaveSymbolMapping, ReloadSymbolMap, Resubscribe, quoteGuard, 量级不符, 符号方言, mt5, 3001157]
scope: [broker_symbol_map, autotrade discover, mt5 adapter, venue=3001157/15032313]
summary: 补符号映射裸写 SQL 只修一半——adapter symMap/订阅不热载；且同名符号跨 venue 可能异底层，quoteGuard 量级闸是权威兜底会实删错配行
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: [生产取证, 协议面取证]
evidence_refs:
  - type: server_fact
    source: postgres
    query: "docker logs nox-core | grep quoteGuard ——「3001157/ADAUSDm → ADAUSDT 量级不符(0.3337 vs ref 0.2240, ratio 1.5)，自动隔离」DOTUSDm/FILUSDm 同型"
    observed_at: 2026-10-21T00:11:00Z
  - type: server_fact
    source: mt5
    query: "MT5/SymbolParams 3001157 ADAUSDm/DOTUSDm/FILUSDm/UNIUSDm 存在(CS=1000/100/50/10)；15032313 WTI_V6_CFD 不存在(V6 合约过期)"
    observed_at: 2026-10-21T00:00:00Z
  - type: doc
    ref: internal/data/bus/quote_guard.go:110
  - type: doc
    ref: internal/api/dashboard/broker_rpc_symbols.go:41
---

## 事实/症状

2026-10-21 armed 开仓前置处置：①裸 `INSERT INTO broker_symbol_map` 后 discover 侧 `SymbolCanonMap`（每拍 DB 重载）生效、unmapped skip 消失，但 adapter 内存 `symMap`（连接时 `symbolPlan` 一次性注入）不热载——SwapFacts 轮询集/quote 订阅集均无新符号，须走 `SaveSymbolMapping` RPC（内部 = Upsert + `bm.ReloadSymbolMap` + `bm.Resubscribe` + 合约零信任校验）或等价触发，否则"补了行不等于活了链"。②RPC 补行后 `quoteGuard` 对 `ADAUSDm→ADAUSDT`(0.3337 vs ref 0.2240, ratio 1.5)、`DOTUSDm→DOTUSDT`(4.66 vs 1.15)、`FILUSDm→FILUSDT`(3.54 vs 1.03) 实删映射"自动隔离"——四源交叉（binance/gate/okx/XMR ticks=0.224）证 EXR 同名 m 尾符号**异底层/异单位**（非该 crypto 现货），守卫判定正确。UNIUSDm→UNIUSDT 过检存活，facts 即时复活。

## 根因或策略解释

- 符号方言第二形态：不只是"后缀不同"——同字母符号（ADAUSDm）在异 broker 可指向**不同底层或不同面额合约**，名称匹配不足为凭，价比（quoteGuard ratio>1.3）才是判据。
- 映射面双写径：DB 行是持久真相，adapter `symMap`/订阅集是运行态影子；裸 SQL 只改前者。
- venue 摘牌合约（WTI_V6_CFD，V6=10 月合约）残帧会每拍刷 `symbol unmapped`——按同族行补 `is_primary=f` 历史变体映射，resolve 后由 2h 鲜度闸诚实丢弃，canonical 去重天然防串价。

## 处置

- 补映射一律走 `SaveSymbolMapping` RPC（buf curl connect 径），合约校验冲突读原因再定 override；死符号/记缺行可 SQL 但须同时声明"不热载，等下次 reconnect/sweep"。
- venue 补映射前先 `SymbolParams` 实拍存在性 + 订阅后首报价比对——异价即停手记缺，不强行入库。

## 证据

- check: SG `docker logs nox-core` + `buf curl DashboardService/SaveSymbolMapping` + grpcurl `mt5grpc3.mtapi.io:443` SymbolParams/IsTradeSessionMany 直取（Connect 后 `-H "Id: <tok>"` 携会话）
- result: 三行被 quoteGuard 自动隔离；UNIUSDm 存活且 swap_long/short facts 90s 内复活；WTI_V6_CFD venue 实拍"Symbol not found"

## 反证条件

- check: 复查 EXR `ADAUSDm` SymbolParams path/description 字段是否实为 ADA 指数/其他计价
- falsifies_when: venue 文档证明 ADAUSDm 即 ADA/USD 现货且报价回归 ~0.22 ——则当时报价为 venue 喂价故障，映射应恢复

## 适用边界

适用于一切 broker_symbol_map 写入径与跨 venue 同名符号配对；cex 侧 WS 订阅另有 cex_stream_manager 径不适用本条目。
