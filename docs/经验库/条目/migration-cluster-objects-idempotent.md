---
schema_version: 2
id: migration-cluster-objects-idempotent
kind: pitfall
risk: high
status: verified
keywords: [迁移, CREATE ROLE, pg_authid, duplicate key, 集簇级对象, 幂等, 并行测试库, CI]
scope: [migrations, test, CI, postgres]
summary: 迁移含集簇级对象（CREATE ROLE/EXTENSION 等）必须幂等——角色不随库建删，并行测试库同跑迁移在 pg_authid_rolname_index 撞 duplicate key，时序敏感间歇红（CI run 38692966418 实证）
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [KB维护]
evidence_refs:
  - type: code
    ref: migrations 1004_nox_ro_role.sql——CREATE ROLE nox_ro 裸写非幂等
  - type: server_fact
    ref: "CI TestD422 挂 migrate v1004: duplicate key value violates unique constraint pg_authid_rolname_index"
  - type: decision
    ref: STATE CI 红两症修票（28e2af737 修后 CI 38694601088 绿）
repro_or_symptom: "CI/并行测试面间歇挂 migrate: duplicate key pg_authid_*——本地单库重跑永远绿（角色已存在则第二库才撞）"
root_cause: PostgreSQL 角色/扩展是集簇级对象不随 database 建删；多测试库并行跑同一迁移=第二个 CREATE ROLE 撞唯一索引
fix_and_prevention: 集簇级对象一律包幂等壳：`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='x') THEN CREATE ROLE x; END IF; END $$`——replay 文件与迁移件同改；评审迁移时把「对象作用域=库级还是集簇级」列为检项
notes: 同类对象清单：ROLE/EXTENSION/TABLESPACE/参数 ALTER SYSTEM；库级对象（表/索引/schema）天然隔离无此险
---

## 现象

CI 间歇挂 `migrate v1004: duplicate key pg_authid_rolname_index`，本地全套绿——时序敏感，只在并行测试库同跑迁移时撞。

## 根因或策略解释

`CREATE ROLE` 等写集簇目录（pg_authid），不随测试库 DROP 清理；并行库共享集簇，第二个迁移必撞。

## 处置

迁移 1004 角色创建改 `DO $$ IF NOT EXISTS ... CREATE ROLE`（28e2af737）；迁移评审新增「集簇级对象幂等」检项。

## 证据

- check: CI run 38692966418 → 38694601088（修后绿）
- result: duplicate key 间歇红消失
- ref: 28e2af737

## 反证条件

- check: 若迁移框架本身串行化集簇级段，本坑不存
- falsifies_when: 测试基建改为每库独立集簇（docker per-db），则幂等壳非必需（仍建议保留防御）

## 适用边界

所有含 ROLE/EXTENSION/TABLESPACE/ALTER SYSTEM 的迁移件；库级对象天然无此险。
