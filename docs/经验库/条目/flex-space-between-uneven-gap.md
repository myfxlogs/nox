---
schema_version: 2
id: flex-space-between-uneven-gap
kind: pitfall
risk: low
status: verified
keywords: [flex, space-between, justify-content, gap, 间距不一致, card-head, CSS, 前端]
scope: [前端, React, CSS flexbox]
summary: flex 容器 justify-content:space-between 按子项数/宽度分配剩余空间，子项数不同的同类容器间对应元素间距不一致；固定间距需将相邻元素包成整体子项（内部 flex gap）。
first_seen: 2026-08-29
last_verified: 2026-08-29
skills: []
evidence_refs:
  - type: commit
    ref: 7575567
  - type: doc
    ref: web/src/styles/08-facts-ledger.css:22
---

## 事实/症状

两个 card-head 容器共用 `.card-head { justify-content: space-between; gap: 8px }`，持仓表 4 个子项、Broker 持仓表 5 个子项。h2 后插入 UtcClock，两处 h2↔UtcClock 视觉间距明显不一致（持仓表窄、Broker 宽）。`marginLeft: -2` 偏移只能局部修正，space-between 重新分配后仍不一致。

## 根因或策略解释

`justify-content: space-between` 把剩余空间均匀分配到子项间隙。剩余空间 = 容器宽 − Σ子项宽，分配份数 = 子项数 − 1。子项数不同→每份宽度不同→对应位置的间隙不同。`gap: 8px` 是最小值，space-between 分配大于 gap 时以 space-between 为准。`marginLeft` 负偏移只移动单个元素，不改变 space-between 的分配算法。

## 处置

将需要固定间距的相邻元素（h2 + UtcClock）包进 `.card-title { display: flex; align-items: center; gap: 6px }`，作为一个整体子项参与父容器的 space-between。内部 gap 固定 6px，不受父容器子项数量影响，两处一致。

## 证据

- check: 两处 card-head 子项数对比（持仓表 4 / Broker 5）
- result: space-between 分配份数 3 vs 4，每份宽度不同→间距不一致
- ref: commit 7575567；web/src/styles/08-facts-ledger.css:22（.card-head space-between）

## 反证条件

- check: 两处 card-head 子项数相同且总宽度相近
- falsifies_when: 子项数相同时间距本就一致，无需包整体（但包整体仍是无害的防御性写法）

## 适用边界

适用于 `justify-content: space-between`（或 space-around/evenly）的 flex/grid 容器中，需要固定相邻元素间距的场景。若容器不用 space-between（仅 gap），则 gap 已是固定值无需此处理。
