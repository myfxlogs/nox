---
schema_version: 2
id: design-sql-must-be-dry-run
kind: pitfall
risk: high
status: verified
keywords: [migration, CHECK 约束, 23514, 42846, 干跑, 存量数据, 回填顺序, bool cast, AVG]
scope: [migrations, workflow]
summary: 设计文档里的 SQL 未在真库干跑即交付施工——加 CHECK 前未对拍存量全集（23514）+ bool::float8 非法 cast（42846），同一 migration 两次炸生产启动
first_seen: 2026-08-21
last_verified: 2026-09-22
skills: []
evidence_refs:
  - type: decision
    ref: D-072 实施修正④
  - type: server_fact
    source: postgres
    query: SELECT id,status,type,opportunity_id FROM positions WHERE COALESCE(type,'')='';
    observed_at: 2026-08-21
---

## 事实/症状

migration 0034（Claude 设计、施工忠实复制）两次炸掉生产启动：① `ADD CONSTRAINT` 排在回填 UPDATE **之后**，且存量 `type=''` 行（数十条）不在新枚举内 → SQLSTATE 23514，容器崩溃循环；② 修复①后第 5 段 `AVG((realized_pnl>0)::float8)` → PG 不允许 bool 直接 cast float8 → SQLSTATE 42846。

## 根因或策略解释

设计期 SQL 只"看起来对"，未在真库/临时库**干跑**；写约束时未先 `SELECT DISTINCT type FROM positions` 对拍存量全集（空 type 早在探针中出现过，设计了回填前缀却漏了空值分支）。测试库未拦截：迁移测试未物理跑到该段。

## 处置

**铁律：设计文档中的每条 SQL（尤其 migration）必须先在真库副本/scratch 库实际执行验证后才进提示词。** 加 CHECK 约束前的固定三步：① `SELECT DISTINCT <col> FROM <表>` 对拍枚举全集；② 所有回填 UPDATE 排在 ADD CONSTRAINT 之前；③ 含聚合/cast 的语句干跑一遍（bool 只能 `::int`，浮点聚合用 `CASE WHEN … THEN 1.0 ELSE 0.0 END`）。

## 证据

- check: 部署日志 `migrate v34: ERROR … violated by some row (23514)` → 修复后 `cannot cast type boolean to double precision (42846)`
- result: 重排回填顺序 + split_part 数据驱动回填空 type + CASE 改写后 v34 applied
- ref: D-072 实施修正④

## 反证条件

- check: 后续设计 migration 是否在干跑后仍出现同类启动失败
- falsifies_when: 连续多个 migration 干跑验证后零启动失败 → 流程稳固，条目降级为 checklist 引用

## 适用边界

所有手写 SQL（migration/回填/聚合查询）。ORM 生成的查询不适用。

**已晋升全局库**：~/kb/pitfalls/2026-08-21-design-sql-must-be-dry-run.md（泛化版，2026-08-21）。
