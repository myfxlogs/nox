---
schema_version: 2
id: overlay-pointer-events-eats-events
kind: pitfall
risk: medium
status: verified
keywords: [overlay, pointer-events, 遮罩, 全屏层, 事件拦截, click target, hover 失效, cursor 死规则, 瞄准模式, 交互 E2E, 代码级审查漏网]
scope: [web, AnalystDrawer, 瞄准模式, 复审]
summary: 全屏 overlay 不设 pointer-events:none 会吃掉点击/hover——事件 target 恒为遮罩自身，closest 捕获落空高亮失效；交互交付须真 UI 实证
first_seen: 2026-08-24
last_verified: 2026-08-24
skills: []
evidence_refs:
  - type: decision
    ref: UI 微调包 F0-F4 复审+部署（decisions.md）
  - type: commit
    ref: b456551
---

## 事实/症状

- 分析员瞄准模式（长按 ✦→点击元素捕获上下文）完全失灵：遮罩+提示语出现，但点任何元素无反应，hover 高亮也不出。
- DOM 结构正常：`[data-ai-type]` 注册点齐全，document capture 监听在——纯 CSS 层问题。

## 根因或策略解释

- `.analyst-aim-overlay` 是 `position:fixed; inset:0` 实体层且未设 `pointer-events:none`——它盖住全页，**所有 click/hover 的 target 都是遮罩自己**，`closest("[data-ai-type]")` 永远 null，`:hover` 规则也落在遮罩上永不触发。
- 伴生①：`cursor:crosshair` 挂在该 overlay 上，转 `pointer-events:none` 后变死规则（不再接收 hover），需挪到 `body.<状态类>`。
- 伴生②：长按计时器触发后未清 ref → 松手走「短按」分支误开抽屉。
- 漏网根因：D-091 主体复审只做了代码级审查+后端 E2E，**交互链路无真 UI 实证**——三根因全部静态可发现但全部漏网，直到业主实测。

## 处置

- 遮罩类组件（瞄准/引导/高亮层）一律 `pointer-events:none`，交互让下层元素接收、document 层监听捕获；需要点击的遮罩子元素单独 `pointer-events:auto`。
- 交互状态类光标挂 `body.<state>` 而非遮罩。
- 长按/短按双态按钮：计时器回调内先清 ref 再置状态。
- 复审检查单增补：**交互链路交付（hover/长按/拖拽/捕获类）必须真 UI 实证**（业主回访或 headless），代码级审查不算闭环。

## 证据

- check: b456551 修复后 bundle 上线，三根因逐条对码（overlay pointer-events:none/计时器清 ref/callout）；生产 ListBrokerPositions alias 注解实证
- ref: decisions.md「UI 微调包 F0-F4 复审+部署」；9f5c0ad→b456551 链

## 反证条件

- check: 若浏览器行为变更（pointer-events:none 层的子元素 auto 不再恢复接收），遮罩内交互按钮方案需重审
- falsifies_when: 设 none 后遮罩仍能成为 click target（规范变更）

## 适用边界

- 通用 Web CSS/交互层；不限框架。遮罩需要自身接收点击的场景（模态阻断层）**不该**设 none——区分「阻断层」与「透视层」。
