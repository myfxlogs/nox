---
schema_version: 2
id: binance-dci-subscribe-param-decimals-refresh
kind: pitfall
risk: medium
status: verified
keywords: [DCI, subscribe, HTTP 400, -1102, -9000, depositAmount, orderId, decimals, refresh, 申购, 双币投资, tryLeg]
scope: [dciexec, binance, DCI 申购下单面, venue 败因面]
summary: Binance DCI 申购三连坑——参数名必须 id|orderId|depositAmount（旧 projectId/lots 致 -1102）、金额按档 min_amount 小数位 floor（USDT 腿=整数，-9000 decimals）、报价/APY 更新后 orderId 须 refresh 重取再提（-9000 refresh）；败因应落机器坑表防重撞（D-255）。
first_seen: 2026-10-11
last_verified: 2026-10-13
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: "fin_dci_order #1-6 response 实拍（SG psql 2026-10-13）：-1102 malformed×2 → -9000 '0 decimals'×3 → -9000 'APY updated refresh'；#7 同参数修正后 filled"
  - type: decision
    ref: D-255
---

# Binance DCI 申购三连坑——参数名/小数位/报价刷新

## 症状

`fin_dci_order` 连续 failed：`subscribe HTTP 400` 三形态轮转，同一申购意图
100 分钟内 6 连撞（#1–6），每轮换 reqID 重发原样撞 venue。

## 根因（生产回执逐单实拍）

| 序 | venue 拒因 | 病灶 |
|---|---|---|
| ① | `-1102 Mandatory parameter 'id|orderId|depositAmount' malformed` | 参数名错代——发 `projectId/lots`，venue 要 `id|orderId|depositAmount|autoCompoundPlan` 四元组 |
| ② | `-9000 Enter an amount to 0 decimals` | depositAmount 带 8 位小数；venue 按档要求整数 USDT |
| ③ | `-9000 Ref. Spot Price and APY updated, please refresh` | 产品报价/APY 已刷新，所持 orderId 陈旧 |

修一层撞下一层——知识沉淀在代码注释（leg.go R10①/R11/R12/R14），
系统自身无记忆：败文躺 `fin_dci_order.response` 死字段零回读、
reqID 尾嵌金额浮点尾（`541.99003584` vs `541.99003600`）破幂等、
`errclass` 无 CEX 码表且 DCI 径不消费。

## 现行修复面（已上线）

- `leg.go SubscribeBody`：四元组参数名正确（R10①）
- `tryleg.go`：`deposit=floorTo(qty, decOf(*q.MinAmount))`——按档 min_amount
  小数位截位（R11/R12），界外 fail-closed 不补不截
- `tryLeg` 内一次 `isRefreshErr`→`refreshOrderRef` 重取 orderId 再提（R14）

## 剩余缺口（D-255 在途）

`fin_venue_reject` 机器坑表未建——新败因仍会「每轮重撞直到人修码」。
判据：下单败行自动落表 + tryLeg 申购前查坑自修复/抑单 + reqID 意图稳定键。

## 证据

- check: SG `psql -c "SELECT id,status,response FROM fin_dci_order ORDER BY id"` #1–6 全 failed，拒因三形态如上表
- result: #7（541 整数+新 orderId）filled→settled；#8 filled
- ref: leg.go SubscribeBody / tryleg.go floorTo+refreshOrderRef 注释行

## 反证条件

- check: 新 DCI 申购再出同形态 HTTP 400
- falsifies_when: 参数四元组+整数金额+新 orderId 下仍 -1102/-9000 → venue 契约又变，重取证

## 适用边界

- 适用=binance DCI（双币投资）申购面；okx/gate DCI 采集面键名不同不套此坑表
- venue 拒因以 response 原文为准逐单取证，勿凭码号猜语义
