---
schema_version: 2
id: ops-script-dsn-permission-mismatch
kind: pitfall
risk: high
status: verified
keywords: [运维脚本, DSN, 只读账号, 权限错配, cron 静默失败, 假绿, .env]
scope: [ops]
summary: 运维脚本「从 .env 读 DSN」前必须核账套权限——.env 里的 DSN 可能是只读 AI 账号，写操作 cron 每天权限拒绝且静默假绿
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-213 审计 M2（docs/D-213-215审计报告-2026-09-04.md）
  - type: commit
    ref: 77703a9（设计稿缺陷）→ 8a61c81（修订）
---

## 事实/症状

D-213 ticks 保留脚本设计稿写「仿 ops_disk_guard.sh 从 .env 读 DSN」。审计核 .env：仅 `ARB_AI_PG_DSN=arb_ro@arb-postgres:5432`（AI 只读），grants 实测 arb_ro 对 ticks/alerts **仅 SELECT**（无 DELETE/INSERT）。施工方照稿实现 = 每天权限拒绝 + cron 输出进日志无人看 = 永久静默假绿（运维版 go-test-dsn-env-skip：清理「看起来在跑」实际从未生效）。

## 根因或策略解释

同一台机器多套 DSN 惯例并存（生产写库 arb:arb@5433 / 容器内只读 arb_ro:5432 / 测试库 5435）。「从 .env 读」的直觉默认 .env 里的就是当前用途的凭据——但 .env 只有一份，装的是 AI 只读账套。guard/backup 等既有脚本全部**写死** arb:arb@5433 正是这个原因（写死=权限意图显式）。

## 处置

运维脚本 DSN 三问后落笔：①该脚本要什么权限（SELECT/DELETE/INSERT）②现有脚本同款 DSN 是哪个账套（backup_pg.sh/guard = arb:arb@localhost:5433 写死）③.env 同名变量是否恰好是只读账套。写操作脚本一律写死写库 DSN+输出行数+失败 exit 1（cron 重定向日志可观测）。「仿现有脚本」要仿到 DSN 字面量，不能仿成「读 env」。

## 证据

- check: `grep -i dsn /opt/arb/.env` → 仅 ARB_AI_PG_DSN（arb_ro）
- check: `SELECT has_table_privilege('arb_ro','alerts','INSERT')` → f；grants 仅 SELECT
- 生产实证：arb=superuser（rolsuper=t），arb:arb@5433 连通性由 backup/guard 每日跑通背书
- 修复后首跑实录：`sh ops_ticks_retention.sh` → `deleted 183 rows` EXIT=0

## 反证条件

若未来 .env 引入与生产写库同权限的专用运维账套（最小权限而非 superuser），写死可改为读该变量——但须在脚本注释显式声明账套权限契约。

## 适用边界

写操作运维脚本（cron 化）。只读脚本读 .env 无此险。

## 关联坑：迁库后 arb_ro 不存在（pg_dump 不搬实例级角色）

2026-10-07 D-105 迁库 SG 实证：单库 `pg_dump/pg_restore` 只搬库内对象，**角色（pg_authid）是实例级**，dump 不含——迁后 `analyst 只读链路断：password authentication failed for user arb_ro (28P01)`，报错写成「密码错」实为角色缺。处置三步：① 回放 `migrations/0041`（幂等建 NOLOGIN+授权）② `tools/ro-rotate` 开 LOGIN+新密+回写 app_secret ③ `SELECT 1` 实连验收（角色存在≠能用）。连带坑：`ro-rotate` 曾缺 `flag.Parse()`，`-verify-addr` 传了不生效（`4bbbdca65` 修）。凡单库 dump 迁移清单须含「实例级角色回放」一步。
