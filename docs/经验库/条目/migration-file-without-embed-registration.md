```yaml
---
schema_version: 2
id: migration-file-without-embed-registration
kind: pitfall
risk: high
status: observed
keywords: [migration, embed.go, migrate.go, schema_migrations, fail-open, false-green-test, D-173]
scope: [pgstore, migrations, 全项目]
summary: migration SQL 文件创建但未在 embed.go 注册 embed var + migrate.go migrationFiles 追加条目 → Migrate() 永不执行该 migration；叠加 fail-open 错误静默忽略 → 测试全绿但功能从未运行
first_seen: 2026-08-31
last_verified: 2026-08-31
skills: [KB维护, 事实核验, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-173
  - type: commit
    ref: bca475e
  - type: commit
    ref: af0528b
  - type: doc
    ref: internal/store/pgstore/migrate.go:14
  - type: doc
    ref: migrations/embed.go:94
  - type: server_fact
    source: postgres
    ref: schema_migrations 缺 version=50 但 product_group 列已手动存在
---
```

## 症状

- 新增 migration SQL 文件（如 `migrations/0050_xxx.sql`）已落盘
- `Migrate()` 日志输出 "migrations applied" 无报错
- `schema_migrations` 表无对应 version 记录
- 对应的 ALTER TABLE 列**可能已手动存在**（之前调试时手动加的），掩盖了 migration 未执行的事实
- 全量 `go test` 全绿——但被测功能从未实际运行

## 根因

migration 落地是**两步操作**，施工方只做了第一步：

1. ✅ 创建 `migrations/NNNN_xxx.sql` 文件
2. ❌ 忘记在 `migrations/embed.go` 加 `//go:embed NNNN_xxx.sql` + `var XxxSQL string`
3. ❌ 忘记在 `internal/store/pgstore/migrate.go` 的 `migrationFiles` 切片追加 `{NN, migrations.XxxSQL}`

`migrationFiles` 是硬编码切片，不是自动扫描目录——漏注册 = migration 永不执行。

## 放大因子

叠加 **fail-open 错误静默忽略**（`productGroupMap, _ := e.store.ListProductGroups(ctx)`）：
- 查询不存在的列 → SQL 报错 → 错误被 `_` 丢弃 → 空 map
- `sameProductGroup("","")` = true（空值放行语义）→ 所有配对通过
- **D-173 product_group 过滤被静默禁用，测试全绿是假象**

## 检测方法

1. 部署后查 `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 3`——最新 version 是否匹配 migration 文件最大编号
2. 查 `information_schema.columns` 确认列存在 **且** schema_migrations 有记录（列存在 ≠ migration 跑过）
3. fail-open 路径必须有错误日志（`log.Printf`），不能 `_` 吞错误——否则 feature-disabled 不可观测

## 修复

- embed.go 加 embed var + migrate.go migrationFiles 追加条目
- SQL 改 `ADD COLUMN IF NOT EXISTS`（幂等，列已存在时不报错）
- fail-open 错误加 `log.Printf`
- 新增 DB 集成测试验证功能端到端生效

## 通用教训

- **migration 注册是两步**：建文件 + 注册（embed.go + migrate.go）。pre-commit 不检查这个——人工审查必须核对
- **fail-open + 错误静默 = 测试假绿**：fail-open 语义正确但错误必须可观测（log），否则功能禁用状态不可见
- **"测试全绿"不等于"功能已验证"**：如果被测路径因上游错误被静默跳过，测试测的是 fallback 路径不是主路径
