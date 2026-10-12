---
schema_version: 2
id: guard-position-refactor-nil-fallthrough
kind: pitfall
risk: medium
status: verified
keywords: [守卫位置, ctx取消, 循环顶守卫, nil落穿, err未赋值, panic, 探针测试, 重构守卫, wsrunner对齐]
scope: [internal/collect/exchange, 任何「循环+错误累积+提前退出守卫」重构]
summary: 把「拨号/执行后才查 ctx.Err()」的守卫搬到循环顶——预取消时零次执行、err 未赋值保持 nil、结果变量保持零值，穿透错误检查后 nil 解引用 panic；对齐既有模式时守卫位置是语义的一部分
first_seen: 2026-09-27
last_verified: 2026-09-27
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-519
  - type: commit
    ref: 87f6da0d
  - type: doc
    ref: internal/collect/exchange/okx_private_ws.go:121
---

## 事实/症状

D-519 R1 把 okx_private 候选循环的 ctx 守卫（原意=对齐 wsrunner.go「dial 后查」）放在了循环**顶部**：
预取消 ctx 进入 runOnce → 零次拨号 → `err==nil`、`conn==nil` → `if err != nil` 放行 →
`conn.WriteJSON` nil 解引用 panic。全量面测试 389 绿（无测试以预取消 ctx 进该函数），探针实证后 R2 修正。

## 根因或策略解释

守卫位置不是风格是语义：循环顶守卫=「跳过整轮」（副作用零、**err 也不赋值**）；
循环内副作用后守卫=「中断当前轮」（err 已由该次失败赋值）。搬移守卫改变了
「函数返回时 err 必已被赋值」这一隐式不变量，nil/零值结果穿透既有错误检查。
测试全绿不能证伪——除非有测试恰好以预取消 ctx 调用（红测 A8 形态）。

## 处置

- 守卫搬移类重构：先写出两位置下「零次执行路径」的变量终态表（err/conn 各是什么），再动。
- 修法=守卫放副作用后（`err != nil` 后查 `ctx.Err()` 提前 break），或循环后补 `if 结果==零值 { return ctx.Err() }` 兜底。
- 红测形态：预取消 ctx 直接调函数，断言「返回 error 非 panic」——比断言具体错误信息更稳。

## 证据

- check: 复审探针（预取消 ctx + -race）→ panic nil deref（R1 形态）
- result: FAIL（实证）
- check: R2 守卫位移正后 TestOKXPrivateWS_PreCanceledCtxNoPanic
- result: PASS（commit 87f6da0d，race 390 绿）

## 反证条件

- 语言层给循环提供「至少执行一次」或守卫语法糖时本文形态失效（当前 Go 无）
- 若函数入口已有「ctx.Err() 预检且显式 return err」则循环顶守卫安全（本文案例入口无预检）

## 参考

- D-519 SSOT=docs/复审-D-519-R2微返修施工提示词-2026-09-27-Claude.md；wsrunner.go:108-115 原版形态
