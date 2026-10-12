---
---
schema_version: 2
id: trongrid-delegatedresourcev2-requires-both-addresses
kind: pitfall
risk: medium
status: verified
keywords: [TronGrid, getdelegatedresourcev2, 委托, fromAddress, toAddress, 静默空, Stake2.0]
scope: [TRON 主网, TronGrid API, 质押监控]
summary: /wallet/getdelegatedresourcev2 必须 fromAddress 与 toAddress 双址同传,只传单址静默返回 HTTP 200 + 空对象 {}(不报错)——按"传 from 查我委托给谁"的直觉集成会拿到永远为空的委托列表。找委托对手方不能扫单址,应走 /v1/accounts/{addr}/transactions 翻 DelegateResourceContract 事件取对手方,再回双址查询确认。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: docs/TRX质押-P0侦察报告-2026-09-03.md §3#3/§6.2
  - type: server_fact
    source: rpc
    query: POST api.trongrid.io/wallet/getdelegatedresourcev2 {"fromAddress":"<owner>"} → {}; {"fromAddress":"<owner>","toAddress":"<counterparty>"} → frozen_balance_for_energy
    observed_at: 2026-09-03T05:50:57Z
    server_revision: mainnet
    response_hash: ""
  - type: decision
    ref: D-210
---

## 事实/症状

`POST /wallet/getdelegatedresourcev2 {"fromAddress":"<owner>","visible":true}` 返回 HTTP 200 + `{}`;同参数补上 `toAddress` 后返回完整委托记录 `{delegatedResource:[{from,to,frozen_balance_for_energy:17270000000}]}`。无任何报错或提示,纯静默空。

## 根因或策略解释

该端点语义是"查 A→B 的定向委托",单址不属于其查询契约;网关对参数不足的查询按空结果处理而非 4xx,与多数 REST 直觉相悖。

## 处置

先经 `/v1/accounts/{addr}/transactions` 扫 DelegateResourceContract/UnDelegateResourceContract 事件得到对手方集合,再对每对 (from,to) 调双址查询确认当前存量;把"双址空结果"与"确无委托"区分开(前者还可能是参数错)。

## 证据

- check: 单址 vs 双址对照调用(同会话、同限频窗口)
- result: 单址 {} / 双址返回 17270000000 sun ENERGY 委托,复验两次一致
- ref: D-210(报告 a606951、审计 d1839eb)

## 反证条件

- check: 构造带真实委托关系的单 fromAddress 查询
- falsifies_when: 单址调用返回该账户全部委托明细(非空),则本条失效

## 适用边界

TronGrid 主网 wallet API(2026-09 实测);Stake 2.0 委托查询族;带宽与能量同构适用(带宽侧未单独实测,同契约假设)。
