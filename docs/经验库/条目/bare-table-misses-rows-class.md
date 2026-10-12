---
schema_version: 2
id: bare-table-misses-rows-class
kind: pitfall
risk: medium
status: verified
keywords: [裸 table, rows 类缺失, 列不对齐, 右侧空白, 表格不撑满, colgroup, table-layout fixed, 跨表列位]
scope: [web, 表格组件, React]
summary: 新增数据表忘加 className="rows"=裸 table——无 width:100%（卡右侧空置）、无右对齐/tabular-nums；同语义多表并列（分节/tab）时 auto 布局致同位列宽各异=跨表列不齐；修法=rows 类+tableLayout:fixed+共享 colgroup 比例列宽
first_seen: 2026-09-22
last_verified: 2026-09-22
skills: [事实核验]
evidence_refs:
  - type: commit
    ref: 575df98c
  - type: decision
    ref: D-397 R1 返修
  - type: doc
    ref: web/src/styles/03-table-chip.css:90
---

## 事实/症状

- 纸面验证仓卡两表是全局唯一裸 `<table>`（D-352 遗留）：卡右侧大量空置（表缩到内容宽）、四轨分表同语义列横向错位——业主目视报「不对称、右边空了太多、列没有对齐」。
- 仓内其余表全部 `className="rows <specific>"` + `tableLayout:"fixed"` + colgroup（pos-active-table 范式）。

## 根因或策略解释

- `table.rows` 全局样式族提供 width:100%/text-align:right/padding/tabular-nums/border——裸 table 一项都吃不到。
- 同语义多表各自 auto 布局 → 列宽按各表内容独立分配，同位列必然错开；只有 fixed+同一 colgroup 才能让不同 `<table>` 的同位列物理对齐。

## 处置

新表三件套：①`className="rows"`（吃全局样式族）②`tableLayout:"fixed"` ③colgroup 百分比列宽；同语义多表共用同一 colgroup 骨架即跨表对齐。复审/目视对「表格不撑满+跨表列不齐」先查是否裸 table。

## 证据

- check: `grep '<table>' web/src/components/` + 业主目视报缺
- result: PositionsPagePaperCarry 两表为唯一裸表；补 rows+fixed+colgroup 后业主确认生效 @575df98c
- ref: D-397 R1 返修 commit 575df98c；样式族 `web/src/styles/03-table-chip.css:90-117`

## 反证条件

- check: 给裸 table 不加 rows 类但逐表 inline style 复制全部样式
- falsifies_when: 若裸 table 的渲染与 rows 表完全等价，则本条目降级（但 inline 复制=重复真相源，仍违反约定）

## 适用边界

- 本项目 React 前端 + 纯 CSS 分层样式表约定下成立；卡片内首列粘性/移动端隐藏列等扩展样式同样挂在 `.table-scroll`/`table.rows` 选择器上，裸表会一并丢失。
