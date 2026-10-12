---
schema_version: 2
id: restartable-loop-closure-resource-semantics
kind: pitfall
risk: high
status: verified
keywords: [GoLoop, panic 重启, defer wg.Done, close of closed channel, WaitGroup 负数, 订阅外置, spawner 重复孵化, 协程失败域]
scope: [safego, goroutine, autotrade, ws, wiring, golang]
summary: 协程转 panic 重启型（GoLoop）时三类资源语义必审计——①once-only 资源（wg.Done/close(ch)）defer 在闭包内=panic 重启重复触发（WaitGroup 负数 panic / close of closed channel）；②通道订阅预建在闭包外=重启读死道白重启；③fn 内嵌子协程孵化（spawner 型 Run）转重启=子协程重复孵化
first_seen: 2026-10-07
last_verified: 2026-10-07
skills: [生产取证]
evidence_refs:
  - type: decision
    ref: D-115
---

## 背景

D-113 事故（positionLoop panic 打死 main:eng 域、退出评估瘫 37min、watchdog 同死零报警）催生 D-115：`safego.GoLoop` panic→告警→退避重启。批量把常驻循环从 `Go` 转 `GoLoop` 时自审捕获三型语义坑——**机械改名不改资源语义 = 引入新 panic**。

## 三型坑（全部实证修于 D-115）

### R1 once-only 资源 defer 在重启闭包内（会 panic）

```go
safego.GoLoop("ws:loop", func() {
    defer wg.Done()          // 错：panic unwind 也触发 → 重启后再 Done → 计数器负数 panic
    defer close(zfDone)      // 错：同上 → close of closed channel panic
    runLoop(ctx)
})
```

修法：**改尾部非 defer**——`runLoop(ctx); wg.Done()`。panic 时跳过，wg 记账从「每次调用」变为「逻辑循环一生」，panic 期间 wg.Wait 正确阻塞到重启后真退出。命中点：binance_market_ws ×5、gate_ws ×2、sched runSource、mt_quote_stream zfDone。

### R2 通道/订阅预建在闭包外（静默失能）

```go
ch, cancel := topic.Subscribe()      // 闭包外
safego.GoLoop("judge", func() {      // panic 时 defer cancel() 已杀订阅
    defer cancel()
    for v := range ch { ... }        // 重启后读死道=白重启
})
```

修法：Subscribe 移入闭包内，每次重启新建订阅。命中点：wire_eventjudge。

### R3 spawner 型 fn 转重启（重复孵化）

fn 体内含 `safego.Go/GoLoop`/`go ` 孵化子协程的（`Run` 包装点、各 *-ws-mgr、sched/posMon 外层）转 GoLoop=每次 panic 重启都再孵一窝子协程→重复 WS 连接/重复 ticker。**spawner 保持 Go**（panic 有告警不重启），子循环自身已 GoLoop 化=失败域正确分层。

## 配套机制

- `Go` 域 panic → `OnPanic(willRestart=false)` → 协程终止；`GoLoop` 域 → `willRestart=true` → 1s×2 退避（封顶 30s，距上次 panic>60s 重置）
- `OnPanic` 用 `atomic.Pointer` 装载而非裸 var——测试实证泄漏 goroutine 的 panic recover 路径与下一测试的钩子赋值并发冲突（DATA RACE）
- main 钩接 `InsertAlertDedup(critical, 30min)`——静默死亡整类消除
