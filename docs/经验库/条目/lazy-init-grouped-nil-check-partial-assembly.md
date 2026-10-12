---
schema_version: 2
id: lazy-init-grouped-nil-check-partial-assembly
kind: pitfall
risk: critical
status: verified
keywords: [nil map panic, lazy init, assignment to entry in nil map, positionLoop, safego, 协程失联, 退出评估停摆, Engine 装配]
scope: [autotrade, engine, wiring, disaster, golang]
summary: lazy-init 用「if 表A==nil{建多表}」合并建表，而生产 New() 只预建表A——三表合并块被跳过、表B 首写即 panic「assignment to entry in nil map」；safego 兜住 panic 但协程已死，测试全用裸 &Engine{}（全 nil→整块初始化）故全绿=夹具与生产装配不同源假绿
first_seen: 2026-10-07
last_verified: 2026-10-07
skills: [生产取证]
evidence_refs:
  - type: decision
    ref: D-113
  - type: commit
    ref: 411c8cbb4
  - type: server_fact
    source: docker-logs
    query: "arb-core 15:46:12 panic recovered: assignment to entry in nil map @engine_disaster.go:182（cexBasisAnchor 首写），栈=scanAndClose→evaluatePosition→checkExitCondition→xassetExitCheck→disasterCheckXAsset→cexBreakDevBps；此后 positionLoop 协程消失，position_evals 停摆 37min"
    observed_at: 2026-10-07T15:46:12Z
---

## 事实/症状

D-113 部署后 36 秒，`disasterCheckXAsset` 首次走到 `cexBasisAnchor[posID]=spread`
写锚 → panic「assignment to entry in nil map」。safego 捕获 panic 打栈但
**positionLoop goroutine 就此消亡**——此后退出评估全链（carry_death/harvest/
drift/disaster/eval 观测行）停摆 37 分钟，直到重新部署才恢复。饥荒看门狗
随协程同死无法自报（自盯盲区）。

## 根因或策略解释

```go
if e.disasterBreach == nil {        // 生产 New() 已建 → 整块跳过
    e.disasterBreach = make(...)
    e.cexBasisAnchor = make(...)    // 永远到不了
    e.mtDivergeFlag = make(...)
}
```

测试构造 `&Engine{}` 字面量时三表全 nil → 整块初始化 → 全绿；生产 `New()`
预建 disasterBreach → 跳过整块的 cexBasisAnchor 仍 nil → 首写 panic。
**夹具与生产装配形态不同源**（同族坑见 gate-api-shape-must-match-sdk R4）。

## 处置

- **每张表各自判 nil**（当前修复形态）或统一在 New() 显式建全——不要
  「以某张表的 nil 代表整组未初始化」这种隐式耦合。
- 新增 map/chan 字段时同步检查两条初始化路径：New() 装配 + 字面量 lazy-init。
- **safego 兜住的 panic ≠ 无害**：loop 协程死后无人重启，watchdog 若在
  同一协程内则同归于尽——loop 型协程的 panic 应落 critical 告警
  （本事件留档为后续立项依据）。
- 回归锚测：构造「New() 同款部分初始化」形态的 Engine 跑判定路径
  （d113_cexleg_test.go A4），不能只用全零字面量。

## 证据

- check: `docker logs arb-core | grep 'panic recovered'`
- result: `assignment to entry in nil map @cexBreakDevBps engine_disaster.go:182`
- check: `SELECT max(evaluated_at) FROM position_evals WHERE position_id=510`
- result: 停摆窗口 15:46:03→16:23:04（37min），hotfix 部署后恢复 ~40s 节拍

## 反证条件

- falsifies_when: 若 New() 与字面量路径共享同一 init helper（单点建表），
  本坑自然消失——届时本条目可标 retired。

## 适用边界

一切「lazy-init 合并建多资源 + 另有 eager 装配路径」的 Go 代码；凡测试只用
零值字面量构造的模块都是易感面。
