---
schema_version: 2
id: test-db-migration-fork-on-renumber
kind: pitfall
risk: medium
status: verified
keywords: [测试库, 迁移重号, schema_migrations, 分支链分叉, 收编重编号, 跨席并行, Migrate v87, 42P01, arb_test, 历史分叉]
scope: [多分支共用测试库, 迁移文件收编时重编号, 跨席复审机检]
summary: 长寿命测试库的 schema_migrations 只记版本号不记内容——分支 A 以 0085 迁移 X 入库后，收编时重编号为 0086（0085 让给迁移 Y），此后分支 B（整链）对同库 Migrate 时 v85 被视为已应用，Y 永不执行，下游 v87 引用 Y 的表即 42P01 报错。表象是"迁移文件明明在链上"。修法=跨分支复审/机检前重置一次性测试库（DROP+CREATE），或以内容哈希记版本。本仓实例=D-273 分支 0085 cex_earn_judgment vs master 整合链 0085 repo_trades。
first_seen: 2026-09-11
last_verified: 2026-09-11
skills: [验证与交接]
evidence_refs:
  - type: doc
    ref: docs/D-280复审-S1S3施工-通过-2026-09-11-ZCode.md（失败甄别 §2）
  - type: server_fact
    source: postgres
    query: "arb_test 重置前 v87 报 relation cnfi_repo_trades does not exist；DROP+CREATE 后同链全绿"
---
