---
schema_version: 2
id: earn-sub-dual-write-double-count
kind: pitfall
risk: high
status: verified
keywords: [earn-sub, 双径记账, io transfer, 订单腿, 残差对账, residual, 双计, gate earn]
scope: [ledger, reconcile, earn, io, residual-recon]
summary: earn 申购同事件被「io transfer 行（earn-sub:*）+订单腿 earnQty」两条台账径各记一次→残差对账权益过解释→critical 假警（gate:earn 16:16 实证 residual −1000.32）；与 xmr-io 异键双写同族第三案
first_seen: 2026-10-18
last_verified: 2026-10-18
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: SG fin_venue_io #1096 gate|transfer|1000.32274381|earn-sub:1028 + fin_dci_order earnQty 1000.32 同事件双落→residual −1000.32 critical
  - type: commit
    ref: 3911efc2c（SumVenueIOTransfers 剔 earn-sub:% 前缀行）
repro_or_symptom: 台账存在双写径（io 流 vs 订单生命周期）且覆盖域相交时，同一经济事件被两条径各解释一次→残差对账「过解释」方向 critical
root_cause: residual 等式 transfers+lifecycle 各自设计覆盖不同事件面（赎回走 io/申购走订单腿），earn 申购件两条径都写——写径互查清单缺失
fix_and_prevention: ①SumVenueIOTransfers 剔 `external_id LIKE 'earn-sub:%'`（申购事件归订单腿径独占解释；赎回无订单腿走 io 不动）②**新增任何 io 写径时必查「同事件是否已有他径解释」**——写径互查清单制③残差对账本身是根治哨兵面（本案即被它抓出）
notes: 异键双写族第三案（xmr-io deal/ticket 双写、镜像余额双计同族）；检测信号=残差值≈单笔申购额整数倍
---

## 现象

earn 申购成交后，`fin_venue_io` 落 `earn-sub:*` transfer 行（写侧记账件产物），同时 `fin_dci_order` 订单腿 earnQty 记同额。residual_recon 等式把两径都计入解释项 → 权益变动被过解释 → critical 假警。

## 根因或策略解释

台账双径（io transfer 流 vs 订单生命周期）覆盖域按设计分工——赎回走 io、申购走订单腿；但 earn 申购写侧件落地时两条径都写了同事件，交集无人核。

## 处置

`SumVenueIOTransfers` 剔 `earn-sub:%` 前缀行（@3911efc2c，已部署 SG）。赎回（`earn-red:%`）无订单腿，io 单计保留不动。

## 证据

- check: SG fin_venue_io #1096（earn-sub:1028 1000.32）+订单腿同额→residual −1000.32
- result: 剔除后同事件单一解释；下一笔申购残差应≤阈（验收锚候自然触发）

## 反证条件

- falsifies_when: 若 earn 申购的订单腿不再记 earnQty（写径变更），剔除会使申购事件零解释→残差反向 critical

## 适用边界

所有「多写径落台账」面：新 io 写径/新订单腿径落地时互查覆盖域交集。
