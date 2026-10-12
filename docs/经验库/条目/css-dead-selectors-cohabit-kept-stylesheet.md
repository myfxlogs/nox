---
schema_version: 2
id: css-dead-selectors-cohabit-kept-stylesheet
kind: pitfall
risk: medium
status: verified
keywords: [死样式, css清理, 删文件族, 选择器census, 逐选择器断言, 样式块迁移, 前端减负, 删除型工单]
scope: [web前端, 纯CSS分层样式表, 删除型改动]
summary: 删除型工单清理 css 时，死选择器与存活选择器共住同一文件（或存活组件的样式块住在被删文件里）——整文件删/整段跳过会同时产生「死类残留」与「误删存活样式」两类缺陷；必须逐选择器 census（grep -oE 导出类名→逐个 grep ts/tsx 消费者+动态构造复核），设计文档的审计性断言（如「全 X 前缀」）须程序化复核，不可凭记忆采信。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-402
  - type: commit
    ref: dcc8c808
  - type: commit
    ref: 16e3d4c2
  - type: doc
    ref: docs/D-402设计与施工提示词-机会分析与价差矩阵两tab摘除-2026-09-23-Devin.md:51
---

## 事实/症状

D-402 摘除机会分析/价差矩阵两 tab。同票两个实证变体：

1. **死类残留**：S3 明令「04-freshness-opp.css 逐选择器 grep——.opp-* 族删，共享类保留」在首轮施工中被漏执行（整文件未动，297 行原样过审面）——复审 R1 抓回（R1-1），34 个死选择器行（.opp-*/.swap-combos-*/.verify-badge*/.combo-chip/.basis-*，唯一消费者=已删组件）滞留 master 候审分支。
2. **存活样式住在被删文件里**：09-matrix.css 非纯矩阵页样式——L237-284 有 .auto-map-* 块 10 规则，正是 S2 移植进 Symbol 映射页的 AutoMapReportCard 所依赖；按 S3 字面整文件删会静默剥掉移植件样式。设计文档二轮自审断言「46 个选择器全 matrix-* 前缀」与事实不符。
3. **census 覆盖面漏网（部署取证轮补获）**：R1 的逐选择器普查只扫了 SSOT 点名的 04-freshness-opp.css——03-table-chip.css 的 `.row-clickable`（原 Overview 机会行点击样式，D-109 深链移除后零消费者）连同「跳机会分析 tab」陈旧注释残留到部署取证才被抓出；教训=普查范围应覆盖**被删交互的全部样式账本**，不止 SSOT 点名文件。

## 根因或策略解释

删除型工单的清理粒度错位：文件是删除单元，选择器是存活单元——两者不对齐时（共住/嵌套），以文件为粒度的执行与核对都会漏。设计文档的人工审计断言无程序化复核时，错误断言会一路传导进验收标准。

## 处置

- 逐选择器 census：`grep -oE '\.[a-z-]+' file.css | sort -u` 导出全部类名 → 逐个 grep ts/tsx 消费者 → 死类删/活类留；动态拼类（如 `fresh-dot-${status}` 模板串）按枚举值域复核。
- 存活样式块住在被删文件 → 先迁后删（D-402 实操：.auto-map-* 块迁 10-broker.css，其余整删）。
- 对设计文档的样式独占性断言跑程序化复核（一行 grep 即可证伪）。

## 证据

- check: `grep -oE '\.[a-z-]+' web/src/styles/04-freshness-opp.css | sort -u` + 逐类名 `grep -rl <cls> web/src`
- result: R1 后 16 token 全存活（fresh-dot 族 3 变体经 FreshDot.tsx `fresh-dot-${dot.status}` 动态构造实证；quote/venue 族逐一在 QuoteStrip/VenueTag 命中）；删除集 34 行零引用
- ref: D-402 R1-1；dcc8c808（漏执行态）；16e3d4c2（回补态）

## 反证条件

- check: 逐选择器 census 通过后做运行时样式取证（部署后页面目视/.dom 断言）
- falsifies_when: census 全过仍出现缺样式回归 → census 模式漏了非模板串量的动态拼接方式，结论需修

## 适用边界

纯 CSS 分层样式表（本仓不用 atomic css 框架）；类名以静态字符串或受控模板串出现。CSS Modules/stylex 等编译期作用域方案不适用。
