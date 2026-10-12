---
schema_version: 2
id: shared-testdb-migration-number-collision
kind: pitfall
risk: high
status: verified
keywords: [迁移号撞车, 共享测试库, migration number, 静默跳过, 并行施工分支, schema_migrations, relation does not exist]
scope: [多席并行施工, migrations, DSN 测试, 收编]
summary: 并行施工分支复用同一迁移号（各占 0085）→ 共享测试库迁移台账按号记账，先到者占号后到者 Migrate 静默跳过建表 → 测试红「relation does not exist」且在分支自树复跑不出来；收编方必须改号（D-273 改 0086 实证，改号即绿）
first_seen: 2026-09-11
last_verified: 2026-09-17
skills: [验证与交接]
evidence_refs:
  - type: doc
    ref: docs/D-273施工复审-通过-两裁定确认-2026-09-11-Claude.md §3
  - type: commit
    ref: b76f415a
  - type: server_fact
    source: postgres
    query: SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 2
    observed_at: 2026-09-11T03:04:00Z
---

## 事实/症状

D-272 与 D-273 两施工分支并行开发，各自新增迁移都编了 0085。D-272 先在共享测试库 arb_test 跑过测试（台账记 85=repo_trades）。复审 D-273 时 `TestCexEarnJudgment_UpsertDedup` 红：`relation "fin_cex_earn_judgment" does not exist`——但 ZCode 自树报告全量 43 包 ok。测试里明明先 `s.Migrate(ctx)` 再 TRUNCATE。

## 根因或策略解释

迁移台账按**号**记账不按内容：arb_test 里 85 已标记应用 → D-273 的 Migrate 认为 85 已做，静默跳过 → 表没建。分支自树（基点早于 D-272、或用未占号的库）复现不出来，施工方的「全绿」是真的、复审方的「红」也是真的——同一代码两个结论。

## 处置

- 收编方整合时**必查最新 master 的 migrations/ 目录最大号**，撞号即改（D-273 0085→0086，migrate.go{N}+embed.go+文件名三处同步）
- 复审遇「relation does not exist + 施工方报全绿」先查共享测试库迁移台账是否被并行分支占号，别急着判施工缺陷
- 生产库无此问题（按序应用，未用过的号都干净）

## 证据

- check: 复审树跑 `go test -run CexEarnJudgment` → 红（relation does not exist）；`git mv 0085→0086`+两处注册改号后同测试 → 绿
- result: 改号即绿，根因闭环
- ref: b76f415a（收编 commit 含改号）；arb_test 台账 SELECT 显示 85 已占

## 反证条件

- check: 各席测试库隔离（一席一库）后重跑
- falsifies_when: 隔离库上仍出现按号跳过，则根因另有其人

## 适用边界

- 适用：本仓多席并行施工+共享 arb_test 的现行工作方式；顺序号迁移（schema_migrations 按号记账）皆同
- 不适用：内容寻址/哈希迁移框架；单席串行开发

## 变体（2026-09-17 Devin 补录）

同库同跑并发撞车：单席全量 `go test ./...` 未加 `-p 1` 时，多 DSN 包（pgstore/dashboard 等）并行各自 Migrate 同一共享测试库 → `duplicate key / already exists` 类迁移竞争红一片（D-318 复审实证 102 红），`-p 1` 串行后 705 全绿。机制同族（共享库迁移台账无并发协调），但触发面更广——不需要分支撞号，同一次测试运行内即可发生。处置：本仓全量测试一律 `go test -count=1 -p 1 -timeout 600s ./...`；库被污染后重建测试库（drop+create from template0）。
