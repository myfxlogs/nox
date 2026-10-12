---
schema_version: 2
id: test-dsn-can-truncate-production
kind: pitfall
risk: high
status: verified
keywords: [测试DSN, 生产库, TRUNCATE, 测试污染, fail-closed, ARB_TEST_PG_DSN, 数据事故, 自愈]
scope: [test, pgstore, autotrade, ops]
summary: 测试助手只防「DSN 未设置→Skip」不防「DSN 指向生产」——设错值即 TRUNCATE 生产表；DSN 门禁必须 fail-closed 判 db 名含 _test，否则拒跑
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/autotrade/accumulator_test.go:21（testAccumStore 空值判断）
  - type: server_fact
    source: 生产 arb-postgres facts 表
    query: venue IN ('vA','vB') 测试行遗留 + 全表被 TRUNCATE 后自愈
    observed_at: 2026-08-25
---

## 事实/症状

测试跑完，生产 facts 表被 `TRUNCATE` 清空、遗留 vA/vB 测试行——测试助手 `testAccumStore` 只写了：

```go
dsn := os.Getenv("ARB_TEST_PG_DSN")
if dsn == "" { t.Skip("ARB_TEST_PG_DSN not set") }
```

**没写「设置值指向生产库怎么办」**。施工方把 `ARB_TEST_PG_DSN` 设成生产 DSN（`postgres://arb:arb@localhost:5433/arb`）后，测试的 `TRUNCATE facts` 直接作用在生产库。

- 事故链：测试 `TRUNCATE` → 生产 swap/margin/trade_mode 全空 → 采集器 1h 周期自愈回填（无历史损失，facts 是滚动现值表）→ 但测试行残留 24h+ 直到被监控日志暴露（`[mt-health] vA=q:n/a/s:24h31m0s`）。
- 影响放大因素：CLOSE_ONLY 恰好生效 → 无交易影响；若引擎 RUNNING，截断窗口内 discoverSwapCombos 零组合（fail-safe 方向，不产生错误交易，但会漏发现）。

## 根因或策略解释

- 环境门控测试的常见假设「DSN 未设置=最坏情况」，但**设置错误值比未设置更危险**——未设置=Skip（无伤害），设置错=写生产（不可逆）。
- `go test` 的 `TRUNCATE`/`DROP`/`DELETE` 无任何库名校验——保护必须自己做。
- 同型兄弟坑：[[go-test-dsn-env-skip]]（未设置静默 skip 的可见性坑）；本条目是它的**反向边界**（设置错值的破坏性坑）。

## 处置

- DSN 门禁 fail-closed 三态：**未设置 → Skip；db 名不含 `_test` → Fatalf 拒绝；否则放行**。db 名判断用 `pgx.ParseConfig(dsn).Database`（不要 strings.Contains(dsn, "_test")——密码含 "_test" 会假放行）。
- 本仓库落地：`internal/testutil.TestDSN(t)` 统一替换 7 处 Getenv 调用点（D-104 P0 返修②）。
- 测试写入型用例的 DSN 一律走此门禁；只读型（analyst tools ReadOnlyDSN）同门禁。

## 证据

- check: 生产 facts 表 2026-08-25 04:29 UTC 被 TRUNCATE（vA/vB 行 ts=now-24h 与测试 `staleTs=now-24h` 精确吻合）；mt-health 日志 `vA=s:24h31m0s` 暴露
- result: DELETE 2 行清污（业主授权）；采集器 1h 内自愈回填；根因=7 处测试助手无库名校验
- ref: 会话 2026-08-25（D-104 P0 部署取证）

## 2026-08-25 第二实例（新变体）

- 生产 `strategy_stats` SWAPARB 行被写入 `disabled=t, disabled_reason='test disabled'`（D-105 施工窗口 09:45）——非 TRUNCATE 而是**状态污染**：引擎 `IsStrategyDisabled` 命中 → 全部 SWAPARB 组合静默跳过（重开后「为什么无机会」的根因）。哨兵信号：`disabled_reason` 含「test」字样=测试值误入生产。

## 反证条件

- check: 门禁上线后再设生产 DSN 跑测试
- falsifies_when: 测试对生产库仍有写行为 → 门禁未覆盖（新测试助手绕过 TestDSN）

## 适用边界

- 一切带 `TRUNCATE`/`DELETE`/migration 的集成测试；只读测试同门禁（防 `pgx.ParseConfig` 前误用）。
- 相关：[[go-test-dsn-env-skip]]、[[construction-claim-needs-machine-check]]。
