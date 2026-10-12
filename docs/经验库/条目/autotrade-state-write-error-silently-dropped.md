---
schema_version: 2
id: autotrade-state-write-error-silently-dropped
kind: pitfall
risk: high
status: observed
keywords: [SetAutotradeState, _ =, 错误忽略, DAILY_LOSS_HALT, 风控失效, 状态机, gate.go, engine_position_close.go]
scope: [autotrade, gate, 状态机, 风控, 日亏损]
summary: 触发 DAILY_LOSS_HALT 和状态机 tick 的 SetAutotradeState 错误被 `_ =` 忽略——状态未持久化且无告警；当前 Gate 仍独立重查日亏并拒绝超限开仓，但状态机防线发生静默退化。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/gate.go:189
  - type: doc
    ref: internal/autotrade/engine_position_close.go:35
---

## 事实/症状

D-188 地基审计发现两处 `SetAutotradeState` 错误被 `_ =` 忽略：

1. `gate.go:189`：日亏损触发 `DAILY_LOSS_HALT` 时 `_ = g.store.SetAutotradeState(ctx, StateDailyLossHalt, ...)`
2. `engine_position_close.go:35`：状态机 tick `_ = e.store.SetAutotradeState(ctx, next, ...)`

**后果链**：
- 日亏损超阈值 → gate 判定应 halt → 调 SetAutotradeState 写 DB → **写入失败被忽略**
- 状态机/运维查询仍可能读到旧状态，且没有故障告警 → 持久状态与真实判定分离
- 当前 `gate.Submit` 每次独立读取 `DailyRealizedPnl`，同一日亏损仍会拒绝开仓；风险是状态防线静默退化，不能断言必然继续开仓

## 根因或策略解释

`SetAutotradeState` 是状态机持久化的唯一入口。gate 和 stateMachineTick 用 `_ =` 忽略错误，可能是早期代码"不阻塞主流程"的简化处理。

但状态机是风控的核心——`DAILY_LOSS_HALT` 是日亏损的最后一道防线。如果这道防线的持久化失败且不报错，系统的持久状态与当前判定分离，运营无法及时知道状态写入失败；当前 Gate 的独立日亏检查仍会拒绝同一日超限开仓。

## 处置

**待修复（D-189 P0-3）**：
- `gate.go:189`：显式检查 `SetAutotradeState`，写失败记录 critical 日志并尝试写 system alert；alert 写失败再次 log。
- `engine_position_close.go:35`：同一错误处理语义，下一 tick 重试，不把未确认写入当作成功。
- 不增加没有恢复协议的内存 halt；Gate 每次 Submit 已独立重查日亏、状态和策略禁用并 fail-closed。

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -n '_ = .*SetAutotradeState' internal/autotrade/`
- result: 2 处（gate.go:189, engine_position_close.go:35）
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认 `SetAutotradeState` 所有调用点是否显式处理错误，且 Gate 是否仍独立执行日亏/状态检查
- falsifies_when: 如果所有写入错误均有可检索告警、状态写入有可靠重试/事务保证，且持久状态与判定不会分离，则本条目的静默故障结论失效

## 适用边界

适用于所有 `SetAutotradeState` 调用点。`STRATEGY_EXHAUSTED` 和 `CLOSE_ONLY` 状态的写入同样受此影响。
