---
schema_version: 2
id: ci-ebadf-ulimit-context-sharing
kind: pitfall
risk: high
status: verified
keywords: [EBADF, bad file descriptor, ulimit, GitHub Actions, race detector, pgxpool, context deadline, CI flaky, fd exhaustion, golang/go#65857]
scope: [CI, Go testing, PostgreSQL, race detector, GitHub Actions]
summary: CI 间歇性 EBADF + context deadline——GitHub Actions ulimit -n=1024 + race detector fd 压力 + 测试 context 被 schema 重建消耗共享超时
first_seen: 2026-08-31
last_verified: 2026-09-07
skills: [验证与交接, 事实核验]
evidence_refs:
  - type: commit
    ref: abd8db4
  - type: commit
    ref: e7b2fa3
  - type: commit
    ref: 8e8a098
  - type: doc
    ref: .github/workflows/ci.yml:47
  - type: doc
    ref: internal/dashboard/investment_records_test.go:35
  - type: commit
    ref: ec0a14a
---

## 事实/症状

GitHub Actions CI 间歇性失败（5 次中 5 次失败），每次失败的测试不同：
- `TestListPersonalPositions_RealFields`: `pgstore.New: ping db: read tcp ...: read: bad file descriptor`
- `TestListPersonalPositions_RealFields`: `TRUNCATE ...: timeout: context deadline exceeded`（30s 后）
- `TestOrderTopicToKickWiring`: `waitForMon` 2s 超时

本地无法复现（ulimit -n=1048576，资源充裕）。

## 根因或策略解释

两个独立根因叠加：

1. **EBADF（bad file descriptor）**：GitHub Actions 默认 `ulimit -n=1024`（soft），race detector 内存 2x+，pgxpool MaxConns=10 在 dashboard 29 处 `pgstore.New` 下 fd 紧张。Go 已知 issue golang/go#65857：`*os.File` finalizer race + race detector 在 fd 压力下触发 EBADF。

2. **context deadline exceeded**：`testServiceStore` 的 30s context 被 `pgstore.New` 和 `TRUNCATE` 共享，但 `rebuildServiceTestSchema`（DROP SCHEMA + Migrate 51 migrations）在两者之间运行。首次调用时 schema 重建消耗大部分 context 时间 → TRUNCATE 剩余时间不足 → 超时。

3. **waitForMon 超时**：CI runner 资源受限 + `-race` 开销，2s 超时不足。

## 处置

1. CI 每个 `go test` step 前加 `ulimit -n $(ulimit -Hn)` 提高到 hard limit（缓解 fd 压力）
2. `testServiceStore` 的 `pgstore.New` 和 `TRUNCATE` 各用独立的 30s context（schema 重建用 `context.Background` 无超时，不消耗任一 context）
3. `waitForMon` 超时从 2s 增到 10s（全部 6 处）

## 证据

- check: CI run 33419631011（修复后）
- result: ✅ 全绿（3m32s，所有步骤通过）
- ref: commit abd8db4 + e7b2fa3 + 8e8a098

## 反证条件

- check: 连续 10 次 CI push 全绿
- falsifies_when: 10 次中出现 ≥1 次 EBADF 或 context deadline → 修复不彻底，需进一步排查 fd 泄漏源

## 适用边界

- GitHub Actions ubuntu-latest runner（其他 CI 环境默认 ulimit 可能不同）
- Go race detector 启用场景（无 race 时 fd 压力减半）
- pgxpool MaxConns ≥10 + 多处 `pgstore.New` 的测试套件

## 衍生场景（D-236 变更9，2026-09-05 二次实证）

**症状**：`TestD236_LiveStateSingleAccessor` 在 CI `-race` 下报 `read service_cmc_watch.go: bad file descriptor`（EBADF），本地无法复现。

**根因**：与主条目同源（ulimit -n=1024 + race detector fd 压力），但触发路径不同——**测试 helper 并发 `os.ReadFile` 读同一源文件**（多个 TestD236_* 用 `readFile` 读 ws.go/service_cmc_watch.go 等）。`-race` 并行测试下多个 goroutine 同时 open 同一文件，fd 压力叠加触发 EBADF。

**处置**：`readFile` 加 `sync.Map` 缓存，每文件只读一次，消除并发 open 竞态（commit `ff84fb1`）。CI 修复后绿。

**通用教训**：CI `-race` 下的源文件读取测试 helper 应缓存文件内容，避免并发 open 叠加到 ulimit 压力上。

## 第三轮实证（D-255，2026-09-07 三次复发）

**症状**：连续两次 CI 红（run 34130623890/34129328577），均在 `go test -race ./...` 全量扫描步骤，dashboard 包两种 flake：①`TestD236_LiveStateSingleAccessor` 读 `service_hedge.go` 触发 EBADF ②`TestOrderTopicToKickWiring` 45s 超时（调度延迟）。

**根因**：dashboard 等 11 个包在集成面①②③④有 DSN 专用步骤真跑，在 `./...` 并行扫描中**重复跑**只加剧 race detector fd/调度压力，无增量覆盖。且第二轮 sync.Map 缓存有盲区——只防同文件并发读，不防跨文件 fd 并发开（多个 goroutine 同时 open 不同文件，fd 压力叠加）。

**处置**：①CI `./...` 扫描排除 11 个有专用步骤的包（`grep -vE`，消除重复跑+并行负载）②`readFile` 加 mutex 序列化文件打开（mutex 补 sync.Map 跨文件 fd 盲区 + double-check）。CI 修复后绿（run 34133850452，4m55s）。

**通用教训**：CI 测试编排应避免 `./...` 全量扫描与专用步骤包重复跑——重复跑无增量覆盖且加剧 race detector 资源压力。源文件读取 helper 的缓存层除 sync.Map（防同文件并发）外，还需 mutex 序列化 open（防跨文件 fd 并发开）。
