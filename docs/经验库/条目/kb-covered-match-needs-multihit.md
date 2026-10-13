---
schema_version: 2
id: kb-covered-match-needs-multihit
kind: pitfall
risk: medium
status: verified
keywords:
  - 经验库归纳
  - covered 误判
  - 关键词对拍
  - noxkb induct
scope:
  - tools/noxkb
  - 经验库管线
summary: 待归纳行对拍条目关键词判 covered 时，单短关键词子串命中不足以定论——须 ≥2 命中或单 ≥12 字符指纹级命中
first_seen: 2026-10-14
last_verified: 2026-10-14
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: induct 管线首落
  - type: doc
    ref: tools/noxkb/induct_match.go
---

## 事实/症状

归纳管线首版用「摘要含任一条目 keyword 即 covered」单命中规则，干跑实拍把「复审 CROSS_VENUE 复活门槛是否过松」误标 `covered:compute-buy-qty-missing-jpy-conversion`（靠关键词 "price_actual"/通用词子串命中）；「binance」一词把深度观察报告行误归 hmac 条目。inbox 行是长文提案非告警指纹，子串命中概率天然高。

## 根因或策略解释

条目 keywords 是检索提示不是排他指纹——短通用词（venue 名/字段名）跨主题出现；单命中语义=「提及」非「覆盖」。自动消解需要高置信才落标（误 covered 代价=真缺口行被打标永不入草稿，比误留更糟）。

## 处置

`matchCovered` 收紧为：每条目计命中关键词数，**≥2 命中**或**单命中且关键词 ≥12 字符**（指纹级报错串/函数名才够格）才判 covered；<4 字符关键词不参与。仍非完美——残留误判靠 LLM 段与人工复核兜（observed 草稿只进候选，verified 只人工授）。

## 证据

- check: 本地干跑 NOX_KB_DIR=<拷贝> noxkb induct，对照 covered 标记逐条人工判
- result: 单命中规则 4 条 covered 中 ≥2 条误判；收紧后 2 条
- ref: tools/noxkb/induct_match.go score 规则注释

## 反证条件

- check: 复审 inbox covered 行断言正确率
- falsifies_when: ≥2 命中仍系统性误判 → 规则升级为 embedding/LLM 判或 covered 段整体退役

## 适用边界

仅适用确定性关键词对拍面（待归纳事件/inbox 行 vs 条目 keywords）；不约束人工分诊。
