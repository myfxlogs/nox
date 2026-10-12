---
schema_version: 2
id: table-ellipsis-needs-fixed-layout
kind: pitfall
risk: medium
status: verified
keywords: [表格错位, ellipsis, table-layout, fixed, colgroup, maxWidth 0, 撑列, 长文本, 表头错位]
scope: [web, 表格组件]
summary: CSS ellipsis 治表格错位必须三件套 table-layout:fixed + colgroup + maxWidth:0/ellipsis——只加 ellipsis 在 auto 布局不生效；提示词写「加 ellipsis」施工方必漏 fixed
first_seen: 2026-08-24
last_verified: 2026-08-24
skills: []
evidence_refs:
  - type: commit
    ref: 897d4c5
  - type: decision
    ref: D-095 实施修正①
---

## 事实/症状

- 验证统计表 vkey 长串撑列错位（897d4c5 修）与持仓表预期复合格撑列错位（D-095 返修②）同病两次；D-095 施工加了 `ellipsis-cell` 类但错位照旧。

## 根因

- `maxWidth:0 + overflow:hidden + text-ellipsis` 技巧**只在 `table-layout:fixed` 下生效**——auto 布局按内容分配列宽，单元格 overflow 约束不了列宽拉伸；fixed 需要 colgroup 分配列宽否则首行内容反客为主。

## 处置

表格错位修法三件套缺一不可：①`tableLayout:"fixed"` ②colgroup 按列分宽 ③长文本单元格 maxWidth:0+ellipsis+title。提示词必须三件套整体点名，禁只写「加 ellipsis」。

## 证据

- check: OpportunityPageVerificationStats（fixed+colgroup 897d4c5 后不再错位）vs PositionsPage（仅 ellipsis 类，D-095 部署后业主复报错位，返修②补 fixed+colgroup）
- ref: decisions.md D-095 实施修正①

## 反证条件

- check: 若仅 ellipsis 类在 auto 布局下也能约束列宽（浏览器行为变更）
- falsifies_when: 三件套齐备后仍错位（另有 colSpan≠列数等结构病——返修②同修）

## 适用边界

- HTML table 场景；CSS grid/flex 布局不适用；展开行 colSpan 必须随列数同步（另一个独立错位源）。
