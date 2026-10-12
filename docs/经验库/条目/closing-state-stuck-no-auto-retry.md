---
schema_version: 2
id: closing-state-stuck-no-auto-retry
kind: pitfall
risk: high
status: observed
keywords: [CLOSING, 卡死, 无自动重试, scanAndClose, ClaimClose, 平仓失败, 中间态, position 状态机]
scope: [autotrade, exec/pipeline, position 状态机, 平仓]
summary: position 进入普通 CLOSING 后若平仓流程失败，autotrade scanAndClose 只扫 OPEN；reconcile 虽扫描 CLOSING 但只告警不重试，缺少按 broker 事实安全恢复的闭环。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/engine_scan.go:13
  - type: doc
    ref: internal/exec/pipeline_legs_close.go:25
  - type: doc
    ref: internal/exec/pipeline_legs_close.go:255
---

## 事实/症状

D-188 地基审计确认 position 状态机存在 CLOSING 恢复缺口：

1. `ClaimClose`（`pipeline_legs_close.go:25`）把 position 从 OPEN/LEGGED → CLOSING
2. 后续平仓流程如果失败（残量校验、verifier 失败、ClosePosition store 写入失败），直接返回 error，**不回滚状态**
3. `scanAndClose`（`engine_scan.go:13`）只扫 `ListPositions(ctx, "OPEN", ...)`；`reconcile/reconcile_close.go:167-181` 虽扫描 CLOSING，但当前只告警
4. 结果：普通 CLOSING 缺少安全自动恢复；UNKNOWN 使用同一 CLOSING 状态，不能混入普通重试

**后果**：
- 持仓卡在中间态，既不平仓也不继续监控
- 保证金继续占用
- 如果市场反向移动，亏损不受控
- 只能人工调用 ClosePosition 或等对账兜底

## 根因或策略解释

position 状态机设计意图是：CLOSING 是短暂的中间态，平仓流程会立即完成。但实际平仓流程是多步骤的异步流程，任何一步失败都会卡住。

`scanAndClose` 只扫 OPEN 可能是为了避免重复处理正在平仓的持仓。但缺少"CLOSING 超时重试"机制——如果 CLOSING 持续超过一定时间，应该自动重触发平仓或告警。

## 处置

**待修复（D-189 P0-4）**：复用现有 reconcile CLOSING 扫描，新增 `closing_started_at`；普通 CLOSING 超时调用 Pipeline `ResumeClose`。UNKNOWN（`uncertainty_reason` 非空）不自动反向下单；重试必须按 `leg_no` 去重、先查 broker、补齐全腿 PnL/现金流后才允许 CLOSED。不新增常驻定时器。

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -n 'ListPositions.*OPEN\|ListPositions.*CLOSING' internal/autotrade/ internal/reconcile/`
- result: autotrade scanAndClose 只查 OPEN；reconcile 已查 CLOSING，但超时分支只有告警无 ResumeClose
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认是否有 reconcile 路径兜底 CLOSING 持仓
- falsifies_when: 如果 reconcile 对普通 CLOSING 先按 broker 事实安全重试、补齐 CLOSE leg/PnL/现金流并只在完整性通过后置 CLOSED，则本条目失效

## 适用边界

适用于所有 position 类型。含 MT 腿的 LEGGED 状态同样无自动收平（`pipeline.go:279-283` 只 autoFlatten 纯 CEX 组合）。
