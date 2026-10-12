---
schema_version: 2
id: soft-warning-comment-compression
kind: pitfall
risk: medium
status: verified
keywords: [注释压缩, 软警告, check-lines, 300 行, 纯搬家, 拆分, R4, D# 注释]
scope: [施工复审, 代码质量, pre-commit]
summary: 用压缩/删减 D# 注释过 >300 行软警告=禁令（R4 四犯实证）——check-lines 是建议非门禁，唯一正解=同包纯搬家拆分；复审用量化平衡法抓
first_seen: 2026-09-08
evidence:
  - type: commit
    ref: 1ba06b2（revert D-256-A 折叠）/96d119e（service_hedge 复原三段）/0144fe7（settlement_elim 复原四行）
  - type: doc
    ref: docs/D-259审计与…同批自审-2026-09-08-Claude.md R4
---

## 事实/症状

同一施工席一日内四次以压缩多行 D# 注释为单行来满足 >300 行软警告（config.go×2/service_hedge.go/settlement_elim 搬家），丢失真实决策语义（如「无限杠杆 2e9→保证金≈0 不告警」「旧式 cum×365 荒谬值显形」）——而 check-lines 门禁本可带警告通过，且同批其他文件用了正确的拆文件法。

## 根因或策略解释

本仓 D# 注释=决策审计轨迹（P3 单一真相源的组成部分）；软警告（⚠️ 建议）被误当硬门禁处理。删史换行数=知识损失，且丢的恰是「为什么」。

## 处置

- 规则：**软警告永远不构成删注释的理由**；超限正解=同包纯搬家拆分（7文件拆分/D-261 先例：注释/代码行删增平衡实证+符号集零差异）。
- 复审量化法：对每个拆分对 `git diff | grep -cE '^-[^-].*//'` vs `'^\+[^+].*//'`——不相等即有注释损耗（D-261 用此法五对全平+一对抓出四行）。
- 收编方复原惯例：cherry-pick 后补复原 commit（当日三次实操）。

## 证据

- check: D-261 六文件拆后全 ≤300 且注释平衡；service_hedge.go 复原后 304 行带警告提交（警告可接受性的活例）
- ref: 会话 2026-09-08（R4→R4'→R4''→R4''' 四连案卷）
