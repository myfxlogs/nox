---
schema_version: 2
id: go-test-dsn-env-skip
kind: pitfall
risk: medium
status: verified
keywords: [go test, DSN, ARB_TEST_PG_DSN, ARB_EXEC_TEST_PG_DSN, ARB_RECONCILE_TEST_PG_DSN, ARB_TEST_PG_SERVICE_DSN, t.Skip, loud-FAIL, 集成测试, 复审, 静默跳过]
scope: [测试, 复审流程, internal/autotrade, internal/exec, internal/store, internal/reconcile, internal/dashboard]
summary: env 门控的 DSN 集成测试缺变量曾静默 skip（D-207 后已 loud-FAIL）；且变量名分包——通用 ARB_TEST_PG_DSN 之外 exec/reconcile/dashboard 要各自专用变量，设错名照样 FAIL；复审必显式设 DSN 且确认 --- PASS 而非 SKIP
first_seen: 2026-08-22
last_verified: 2026-09-06
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/accumulator_test.go:22
  - type: commit
    ref: 26e44ed
---

## 事实/症状

修复⑬返修包②复审（2026-08-22）：先跑全量 `go test ./... -count=1` 全绿，险些据此出结论——实际上 R8 两笔 DSN 测试（ConcurrentNoRace/DisabledStillFeedsRollup）全部 SKIP，根本没跑。测试 helper 首行 `os.Getenv("ARB_TEST_PG_DSN")=="" → t.Skip`，go test 把 skip 计为通过。

**演进（D-207 loud-FAIL，2026-09-06 复审实证）**：缺 DSN 已改为 loud-FAIL 不再静默 skip。新坑=**变量名分包**：只设通用 `ARB_TEST_PG_DSN` 跑 exec 包仍 FAIL——`pipeline_legs_persist_rollback_test.go:187` 要的是 `ARB_EXEC_TEST_PG_DSN`（报错信息会明示）。复审者须按包核对变量名，勿假设一个通用变量通吃。

## 根因或策略解释

env 门控的集成测试用 t.Skip 是标准做法（CI 无库时优雅降级），代价是「ok」结果掩盖「未验证」——D-207 已用 loud-FAIL 根除（缺 env=FAIL，假绿不再长得像过了）。残余坑是变量名 fragmentation：各包独立引入门禁时各自命名，复审者设了「显然正确」的通用名仍被拒。

## 处置

复审 DSN 测试：① 复审库走 :5433 容器映射（宿主 5432 是宿主级 PG，见 host-pg-occupies-5432-container-on-5433）；② 变量名清单（2026-09-06 全仓 grep 盘点）：`ARB_TEST_PG_DSN`（通用）+ `ARB_EXEC_TEST_PG_DSN`（exec）+ `ARB_RECONCILE_TEST_PG_DSN`（reconcile，integration tag）+ `ARB_TEST_PG_SERVICE_DSN`（dashboard investment_records/watchlist 单例 store，D-234 批 1 起 loud-FAIL）+ `ARB_TEST_ALLOW_SKIP_DSN=1`（自觉跳过开关）；本地复审四变量同设（CI 面③已注入 SERVICE_DSN）；③ 新包跑 FAIL 且报「XXX 未设置」时照报错补变量，勿判施工缺陷；④ 输出确认目标测试 `--- PASS` 而非 `--- SKIP`，另 grep -c '^--- SKIP' 应为 0。

## 证据

- check: `grep -n "ARB_TEST_PG_DSN" internal/autotrade/accumulator_test.go`
- result: :22-25 dsn=="" → t.Skip（D-207 前）
- 复现实录：无 env 全量绿 → 设 env -v 后 ConcurrentNoRace 0.26s / DisabledStillFeedsRollup 0.10s 真 PASS
- loud-FAIL 实录（2026-09-06）：仅设 ARB_TEST_PG_DSN 跑 exec 包 → FAIL「ARB_EXEC_TEST_PG_DSN 未设置——DSN 缺失不再静默 SKIP」；补设后 5.9s ok
- ref: docs/修复⑬返修包②-施工提示词.md §机检与验收

## 反证条件

- check: 各包变量名收敛为单一 ARB_TEST_PG_DSN 或 CI 统一注入
- falsifies_when: 全仓仅剩一个 DSN 变量名

## 适用边界

所有 DSN 门控包（autotrade/exec/pgstore/dashboard/reconcile/cmc）；复审/验收角色跑机检时必查，变量名以全仓 grep 为准。
