---
schema_version: 2
id: fk-new-table-breaks-truncate-fixtures
kind: pitfall
risk: medium
status: verified
keywords: [外键, FK, migration, TRUNCATE, 测试fixture, 0A000, 级联删, 迁移评审]
scope: [store层, migration, 测试基建, postgres]
summary: 新迁移建带外键（REFERENCES 父表 ON DELETE CASCADE）的子表，会打断既有测试对父表的 TRUNCATE（SQLSTATE 0A000 cannot truncate a table referenced in a foreign key constraint）——TRUNCATE 是 DDL 级清表不认行级级联；迁移评审时应全仓 grep 父表名的一切 TRUNCATE 站点（测试 fixture 与运行时清理），命中则同迁移补列/改 CASCADE 或放弃 FK 改应用层完整性。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-403
  - type: commit
    ref: 8d03ccef
  - type: doc
    ref: migrations/0125_carry_graduations.sql
---

## 事实/症状

D-403 S1 建 carry_graduations（REFERENCES paper_spot_carry ON DELETE CASCADE）——scan/dashboard 两处既有测试 fixture `TRUNCATE ... paper_spot_carry ...` 全数 0A000 红（23 用例级联失败），运行时无 TRUNCATE 站点（grep 实证）故生产面无损。

## 根因或策略解释

TRUNCATE 是 DDL 级整表操作，绕过行级 ON DELETE CASCADE；PG 只在显式列出全部引用表或带 CASCADE 关键字时放行。带 FK 的新表=对父表一切 TRUNCATE 站点的硬破坏面，而 TRUNCATE 常见于测试 fixture（清态）与保留期清理——迁移设计时只看运行时语义不看测试清态惯例即漏。

## 处置

迁移评审清单加一项：新表带 FK → `grep -rn "TRUNCATE.*<父表>"` 全仓（含 *_test.go 与清理任务）→ 命中则三选一：①引用表列入各 TRUNCATE 清单（D-403 实操：两 fixture 补列 carry_graduations）；②TRUNCATE 加 CASCADE 关键字（放水过广慎用）；③去 FK 改应用层完整性（引用完整性弱化，慎用）。运行时清理站点若有周期 TRUNCATE 父表的保留策略，FK 是生产事故面须优先处置。

## 证据

- check: `grep -rn "TRUNCATE" --include="*.go" . | grep <父表>`
- result: 两测试 fixture 命中（d350_spot_carry_test.go:21 / service_paper_spot_carry_test.go:23）；补列后触及包四 DSN -p 1 全量 0 FAIL
- ref: D-403 R1（8d03ccef 申报偏差①）；R2 复审裁定有效入 KB（51d47584）

## 反证条件

- check: 新增 FK 子表时按本清单 grep 后仍有漏网 TRUNCATE 站点红
- falsifies_when: grep 模式覆盖不到非字面拼接的 TRUNCATE（动态 SQL 拼表名）——需辅以 0A000 错误监控定性

## 适用边界

PostgreSQL（TRUNCATE 语义各异）；父表被 TRUNCATE 的场景（测试清态/保留清理）；ON DELETE CASCADE 不救 TRUNCATE 是 PG 固有行为。
