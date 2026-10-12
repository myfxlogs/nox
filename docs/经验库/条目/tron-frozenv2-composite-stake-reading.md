---
---
schema_version: 2
id: tron-frozenv2-composite-stake-reading
kind: pitfall
risk: high
status: verified
keywords: [TRON, TronGrid, frozenV2, 质押量, 委托, tronPower, getaccount, 质押监控, Stake2.0]
scope: [TRON 主网, TronGrid API, 质押监控, 个人持仓监控行]
summary: getaccount.frozenV2 只呈现未委托余量——资源 100% 委托出时该类型项 amount 为空,直读会把"在押但已委托"的部分整个漏掉;质押总量=ΣfrozenV2 非空 amount + account_resource.delegated_frozenV2_balance_for_energy(带宽侧同名字段同理),并用 getaccountresource.tronPowerLimit 交叉校验。实证:业主地址 frozenV2=[BANDWIDTH 800, ENERGY 空],实际在押 18070=800+17270(能量全委托),votes=tronPowerLimit=18070。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: docs/TRX质押-P0侦察报告-2026-09-03.md §2/§6.1/§8
  - type: server_fact
    source: rpc
    query: POST api.trongrid.io/wallet/getaccount {"address":"TWsbXR6SPvvXPxUtPMQvEj7AMH55555555","visible":true}
    observed_at: 2026-09-03T05:50:57Z
    server_revision: mainnet
    response_hash: ""
  - type: decision
    ref: D-210
---

## 事实/症状

`getaccount` 返回 `"frozenV2":[{"amount":800000000},{"type":"ENERGY"},{"type":"TRON_POWER"}]`——ENERGY 类型实际在押 17270 TRX 但 amount 缺席。若把 frozenV2 当质押总量直读,报出 800 TRX,而真实在押 18070(votes 与 tronPowerLimit 均为 18070),质押监控低估 95%。

## 根因或策略解释

frozenV2 的 amount 呈现与委托状态耦合:已委托出的部分不在 frozenV2 amount 中呈现,单独记在 `account_resource.delegated_frozenV2_balance_for_energy`(方向=本账户委托出)。方向由两条独立证据钉死:链上 DelegateResourceContract 事件(16048+1222=17270,与字段值精确相等)与 `getdelegatedresourcev2` 双址查询(from=owner,to=受托方,返回 frozen_balance_for_energy=17270000000)。

## 处置

质押总量 = ΣfrozenV2 非空 amount + account_resource.delegated_frozenV2_balance_for_energy(+带宽侧 delegated 同名字段若有);每轮用 `getaccountresource.tronPowerLimit`(=votes 上限)交叉校验,不一致即告警。相关坑见 `trongrid-delegatedresourcev2-requires-both-addresses`。

## 证据

- check: 新鲜重拉 getaccount + getdelegatedresourcev2 双址 + 全量链上交易史(514 笔,翻穿至账户生日)重算冻结和解押合计
- result: ENERGY 冻结 20014 − 唯一解押 2744 = 17270 ✓;BANDWIDTH 冻结 700+100=800 ✓;800+17270=18070=votes=tronPowerLimit ✓;NetLimit=43.2e9×800/26,869,037,200=1286.4≈实测 1286 ✓
- ref: D-210(报告 a606951、审计 d1839eb)

## 反证条件

- check: 找一个资源 100% 委托出的在押账户,观察其 frozenV2 对应类型项是否带完整 amount
- falsifies_when: 全委托账户的 frozenV2 ENERGY amount 仍完整呈现(=17270000000),则拼合规则过度,直读成立

## 适用边界

TronGrid getaccount 的 JSON 编码路径(2026-09 主网实测);自建 java-tron 节点同 protobuf,JSON 序列化实现可能不同,未验。仅覆盖 Stake 2.0(V2 字段);Stake 1.0 的 `frozen`/`account_resource.delegated_frozen_balance_for_energy` 呈现语义另测。
