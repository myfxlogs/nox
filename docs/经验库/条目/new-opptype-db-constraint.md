---
schema_version: 2
id: new-opptype-db-constraint
kind: invariant
risk: medium
status: verified
keywords: [PERP_FUNDING, CHECK, migration, opportunities.type]
scope: [store, migrations, detector]
summary: 新增机会类型必须同步 DB CHECK 约束（PERP_FUNDING 检测器上线而约束未更新 → upsert 失败，migration 0032 补）
first_seen: 2026-08-20
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: D-068
  - type: commit
    ref: 30b4219
---

## 事实/症状

PERP_FUNDING 检测器（D-064）上线后，opportunities 表的 `type` 列 CHECK 约束仍为 `('CROSS_VENUE','CARRY','FUNDING_HEDGE')`，不含 `PERP_FUNDING` → upsert 新类型机会时 DB 约束失败。

## 根因或策略解释

DB CHECK 约束是 opportunities.type 的枚举守门。新增检测器产出新 type 时，如果不同步更新 CHECK 约束，upsert 会被 DB 拒绝。检测器代码层不报错（type 是 string），但持久化层静默失败。

## 处置

新增机会类型时必须同步：① 检测器代码 ② DB CHECK 约束（migration） ③ 架构设计 §2.6 文档（或收缩为指针）。migration 0032 补加了 PERP_FUNDING。

## 证据

- check: `grep -n "PERP_FUNDING" migrations/0032_perp_funding_type.sql`
- result: migration 0032 将 PERP_FUNDING 加入 CHECK 约束
- ref: D-068 + commit 30b4219

## 反证条件

- check: 审查 type 校验是否移回应用层且 DB 撤约束
- falsifies_when: type 校验移回应用层且 DB 撤约束（单一真相迁移，须 D# 留痕）

## 适用边界

opportunities 表 type 列，所有新增机会类型的检测器上线流程。
