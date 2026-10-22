---
schema_version: 2
id: venue-ratelimit-same-second-burst
kind: pitfall
risk: high
status: observed
keywords: [429, 限频, 同秒连发, User ID 规则, okx, sfp/dcd/order-history, kick drain, rate limit]
scope: [venue, okx, 限频定性, kick drain 批内并发]
summary: okx /sfp/dcd/order-history 文档档=1 req/s（User ID 规则）——429 连败真凶=同秒连发（批内多 venue 键串行 poll 各发一次 GET）非速率超限；0.028/s 远低于档却连败 22 次。限频定性必先拿文档档+生产窗口计数对拍
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳-分析员.md（行200，okx 429 修坑候选批）
---

## 现象

okx 接口 429 连败 22 次，实测窗口速率仅 0.028/s——远低于文档档却连败。真凶=
kick drain 批内多个 venue 键串行 poll，各发一次 GET=**同秒连发**，命中
「1 req/s（User ID 规则）」的粒度是「同秒」非「平均速率」。

## 判别锚（验收硬件）

- 限频定性前两件缺一不可：①官方文档档（每秒/每分钟/User ID 维度）②生产窗口
  实测计数（请求时间戳分布）——只见 429 不见计数不可定性「超频」。
- 429 语义=罚量倍增+成功减半衰减；skip 不占时隙。

## 根因

限速器文档档粒度（同秒一票）与直觉口径（窗口平均速率）错位；批内并发/串行
poll 形态把低速率请求压进同一秒。

## 处置

时隙预约必须「上个时隙续排」非 `now+min`（后者让排队者挤同一时隙——首版
bug 实证 3 连发 0/50/50ms 非 0/50/100）。

## 适用边界

venue 限频类定性全谱适用（okx 实证，binance/gate 同形先查档再定性）。
