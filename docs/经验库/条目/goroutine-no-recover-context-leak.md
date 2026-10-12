---
schema_version: 2
id: goroutine-no-recover-context-leak
kind: pitfall
risk: high
status: observed
keywords: [goroutine, defer recover, panic, 崩溃全进程, context.Background, 泄漏, 长生命周期, BackfillEntrySpreadPublic]
scope: [cmd/core, adapter, dashboard, 所有长生命周期 goroutine]
summary: 所有长生命周期 goroutine 无 defer recover——单个 panic（nil map/数组越界）崩溃整个进程；BackfillEntrySpreadPublic 用 context.Background()，服务关闭时无法退出。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: cmd/core/setup.go:158
  - type: doc
    ref: cmd/core/setup.go:160
  - type: doc
    ref: cmd/core/wire_dashboard.go:65
  - type: doc
    ref: internal/decimalutil/decimalutil.go:25
---

## 事实/症状

D-188 地基审计发现：

1. **无 recover**：所有 `go func()` 启动的长生命周期 goroutine（quote 流、order events、scheduler、ledger、reconcile、position monitor 等）均未 `defer recover()`。单个 panic（nil map 访问、数组越界、类型断言失败）会崩溃整个进程。

2. **context.Background 泄漏**：
   - `setup.go:160`：`BackfillEntrySpreadPublic(context.Background())` — 无超时/取消，服务关闭时无法退出
   - `wire_dashboard.go:65`：`stepMeta.MaybeRefresh` 用 `Background()` — 无 `ctx.Done` 检查

3. **MustFromFloat64 生产调用**：`decimalutil.go:25` 的 `panic(err)` 被 `swapledger/ledger_helpers.go:83,86` 生产调用——理论上不会 NaN，但 `Must*` 与"仅用于测试/常量"的注释矛盾。

## 根因或策略解释

Go 的 goroutine panic 如果没有 recover，会直接崩溃整个进程。长生命周期 goroutine（如报价转发、持仓监控）运行在整个进程生命周期，遇到任何 panic 都会杀死所有其他 goroutine。

`context.Background()` 创建的 context 永远不会被 cancel，用它的 goroutine 无法响应服务关闭信号。

## 处置

**待修复（D-190 P1-3）**：
1. 所有长生命周期 goroutine 加 `defer func() { if r := recover(); r != nil { log.Printf("goroutine panic: %v\n%s", r, debug.Stack()) } }()`
2. `BackfillEntrySpreadPublic` 和 `MaybeRefresh` 改用主 ctx
3. 生产代码替换 `MustFromFloat64` 为 `FromFloat64` + error 处理

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -rn 'go func()' cmd/core/ internal/ | grep -v _test`
- result: 大量 goroutine 无 recover
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认是否有顶层 recover（如 main 函数 defer recover）
- falsifies_when: 如果 main 函数有 defer recover 捕获所有 goroutine panic（Go 不支持——子 goroutine panic 不被父 recover 捕获），则本条目失效（但 Go 语言层面不支持）

## 适用边界

Go 语言层面：子 goroutine 的 panic 不被父 goroutine 的 recover 捕获。每个 goroutine 必须有自己的 recover。
