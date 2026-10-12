---
schema_version: 2
id: pgxpool-test-exhaustion
kind: pitfall
risk: high
status: verified
keywords: [pgxpool, Acquire, pool_max_conns, max_connections, 测试超时, sync.Once, 单例 pool, CI flaky, race detector, 53300, too many clients, 逐包库并行]
scope: [Go 测试, pgxpool, PostgreSQL, dashboard 包, DSN-gated 测试]
summary: 同包多测试各自 pgstore.New 创建独立 pool 累积耗尽 pg 服务端 max_connections → Acquire 卡住 → 30s 超时 flaky；修法=包级单例 pool（sync.Once）
first_seen: 2026-09-03
last_verified: 2026-09-22
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: c33f33c
  - type: commit
    ref: 6c21fac9
  - type: doc
    ref: internal/dashboard/investment_records_test.go:19
  - type: doc
    ref: internal/dashboard/service_live_gate_test.go:14
---

## 事实/症状

dashboard 包全量 `go test -race` flaky：每次卡在不同测试（TestCheckLiveGate_RealVenue_Rejected /
TestCheckSwapDeviationGate_HighDeviation_Rejected / TestGetHedgeStructure_MirrorPnlSumZero 轮换），
goroutine stack 显示 `pgxpool.Acquire` 卡在 `puddle.Pool.acquire`（select 等资源）。
单测单独跑过；空库 DROP+CREATE arb_service_test 后第一次跑可能 5s 快速失败，第二次 44s 超时。

## 根因或策略解释

同包 8 个 DSN-gated 测试 helper 各自 `pgstore.New(dsn)` 创建独立 pgxpool（`pool_max_conns=2`），
`t.Cleanup(s.Close)` 在测试结束才关——race detector 下 Close 异步，连接未立即释放。
多测试累积耗尽 pg 服务端 `max_connections` → 后续测试 `pgxpool.Acquire` 卡住 → 30s 超时 flaky。

关键：Go test 同包默认串行（无 t.Parallel），但串行不等于 pool 立即关闭——t.Cleanup 在测试函数
返回后才跑，pgxpool.Close 异步释放连接，下一个测试 New 时旧 pool 的连接可能还 idle。

## 处置

包级单例 store（`serviceTestStoreSingleInit` sync.Once）：
- `investment_records_test.go` 新增 `serviceTestStoreSingleInit`：sync.Once 初始化单例
  `pgstore.Store` + Migrate，进程结束自动 Close（无需 t.Cleanup）
- 8 个测试 helper 全部改用单例（trxTestStore/setupMeanRevertService/
  setupAutotradeStateStore/setupSwapDeviationServiceStore/setupBackfillServiceStore/
  setupLiveGateServiceStore/TestBackfillRuleMappings_MockSymbolsSource/testServiceStore）
- `rebuildServiceTestSchema` 改 no-op（单例已 Migrate，保留兼容签名）
- 删除未使用 imports（pgx/strings/os/testutil）

验证：CI 三步（pgstore+autotrade `-p 1` / exec / dashboard）race 全绿×5 次稳定
（空库 DROP+CREATE arb_service_test 每次重建）。

## 证据

- check: `go test -race -count=1 -timeout 30s ./internal/dashboard/` 修复前
- result: panic: test timed out after 30s，goroutine stack 显示 `pgxpool.Acquire` 卡在 puddle select
- check: 修复后 5 次连续 `go test -race -count=1 -timeout 60s ./internal/dashboard/`
- result: 5 次全绿（3.6s/2.9s/2.9s/2.7s/3.0s），稳定无 flaky
- ref: commit c33f33c

## 反证条件

- check: 改回每测试独立 pgstore.New + t.Cleanup(s.Close)，跑 5 次
- falsifies_when: 5 次全绿无超时 → 说明根因不是 pool 耗尽（可能是其他如 pg max_connections 提升了）

## 跨包形态（2026-09-22 D-383C 实证补充）

D-351 逐包库落地后全仓 `go test ./...` 并行：每包独立库（arb_test_<tag>）连接数=包数×pool，
pg 服务端 max_connections 被并行包集体打满 → 失效形态变为 **SQLSTATE 53300
（too many clients）立即拒绝**，非本条同包形态的 Acquire 卡超时；败面逐轮换包
（两轮全仓跑均红于 internal/collect/trx，第三轮换 judge——judge 另查为凭据缺失独立项）。
甄别与处置同 shared-testdb-120s-lock-flake-isolated-green：53300 错误面+孤立复跑绿
（trx/dashboard/exec 三包单独跑全绿）即归资源上限，非代码红；补救向=收敛并行度
（`-p <N>` 或分批跑触碰 DSN 的包），非改代码。

## 适用边界

- 适用于同包多测试共用同一 DSN 的场景（如 dashboard 包 8 个 helper 共用 arb_service_test）
- 不适用于测试需独立 DB 隔离的场景（如 pgstore 包测试用 prepareTestSchema DROP+Migrate）
- 单例 pool 要求所有测试 TRUNCATE 自己的表做数据隔离（不能依赖 schema 重建）
- CI 环境需全新库（首次 Migrate 建表）；本地已 migrate 的库幂等跳过
