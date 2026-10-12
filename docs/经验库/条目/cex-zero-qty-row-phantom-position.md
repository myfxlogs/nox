---
schema_version: 2
id: cex-zero-qty-row-phantom-position
kind: pitfall
risk: medium
status: verified
keywords: [CEX, 平仓, 零量行, pa=0, 幻影仓, 全量替换, ACCOUNT_UPDATE, a.P, PositionEventCache, binance]
scope: [internal/collect/exchange, binance, okx, gate, 持仓事件缓存, dashboard 实持仓]
summary: venue 平仓后在 ACCOUNT_UPDATE a.P（及同构持仓事件）推零量行（pa="0"）——全量替换语义下照建 Volume=0 VenueOrder → 缓存/dashboard 实持仓表出现已平符号幻影行；修=零量条目 continue 跳过（不建仓、iwSum 不累加），不建仓即「该 symbol 无仓」的正确全量替换语义。
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: 1f4e162b
  - type: doc
    ref: internal/collect/exchange/binance_user_ws_events.go:210
---

## 事实/症状

bntesttrade -live 真开平实证：平仓后 venue 推 `a.P` 元素 `"pa":"0"` 行，`onAccountUpdate` 照建 `Volume=0` 的 `VenueOrder` 写入 `PositionEventCache` → dashboard 实持仓表出现已平符号的幻影行（零量仓挂显）。

## 根因或策略解释

venue 语义：`pa=0`（零量持仓行）=「该 symbol 无仓」，是平仓通知的载体而非可持仓条目；消费端把「零量行」当「持仓条目」照建，违背缓存的全量替换契约——替换集内包含零量条目即幻影。

## 处置

`a.P` 循环解析 pa 后 `if pa == 0 { continue }`——不建仓、`iwSum` 不累加该条；全量替换语义下替换集不含该 symbol = 旧仓自然清除。

## 证据

- check: TestAccountUpdateZeroPa——开仓帧（pa=16）建仓 → 平仓帧（pa=0）→ posCache 该 symbol 应无仓
- result: 修前幻影行在缓存（测试红）；修后无仓（绿）；bntesttrade -live 平仓实证残留行
- ref: commit 1f4e162b（D-584 S4，b51a8c61 扩面增收）

## 反证条件

- check: 若某 venue 零量行语义=「仍有仓但量为 0」（非平仓通知）
- falsifies_when: venue 文档/实测表明 pa=0 行须保留为可见条目

## 适用边界

适用全量替换型持仓缓存（PutPositions 类）的所有 venue 事件源（binance a.P 实证；okx/gate 同构持仓事件面同查）；增量 patch 型缓存不适用（零量行应触发该 symbol 删除而非跳过）。
