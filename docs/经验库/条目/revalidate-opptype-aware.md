---
schema_version: 2
id: revalidate-opptype-aware
kind: pitfall
risk: high
status: verified
keywords: [Revalidate, FUNDING_HEDGE, 误拒, oppType, 价格差复核]
scope: [exec]
summary: Revalidate 用价格差复核息差型（FUNDING_HEDGE）恒误拒 → 复核必须按机会类型分型跳过
first_seen: 2026-08-20
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: D-068
---

## 事实/症状

Revalidate 用价格差复核 FUNDING_HEDGE（息差型）机会时恒误拒——息差型机会的利润来源是 funding rate 差而非价差收敛，价格差复核逻辑不适用。

## 根因或策略解释

Revalidate 原设计只考虑价差型（CROSS_VENUE）机会的复核语义：用当前报价重算毛收益，劣化超阈值则拒绝。息差型（FUNDING_HEDGE）的利润主体是持仓期 funding rate 差，入场时价格差与利润无直接关系——用价格差复核恒误拒。

## 处置

复核必须按机会类型分型：价差型走价格差复核，息差型跳过价格差复核（或改用 funding rate 差复核）。

## 证据

- check: 审查 decisions.md D-068 修正②
- result: D-068 demo 实证中 Revalidate 用价格差复核 FUNDING_HEDGE 误拒 → 改按 oppType 跳过
- ref: D-068

## 反证条件

- check: 审查息差型机会是否引入显式价格收敛复核语义
- falsifies_when: 息差型机会引入显式价格收敛复核语义

## 适用边界

执行层 Revalidate 逻辑，适用于 CROSS_VENUE / FUNDING_HEDGE / CARRY / PERP_FUNDING 等不同机会类型的复核分型。
