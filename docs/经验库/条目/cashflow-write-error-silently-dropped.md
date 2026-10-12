---
schema_version: 2
id: cashflow-write-error-silently-dropped
kind: pitfall
risk: high
status: observed
keywords: [InsertCashflow, _ =, 错误忽略, realized_pnl, netPnl, swap 现金流, commission 现金流, pipeline_legs_close, reconcile, 账本错误]
scope: [exec/pipeline, reconcile, settlement, PnL 计算, 平仓]
summary: 平仓时 InsertCashflow（swap/commission）的写入错误被 `_ =` 吞掉——现金流缺失会使 netPnl/结算统计缺组件，但不直接改变价格差 realized_pnl，且原错误不报错。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/exec/pipeline_legs_close.go:182
  - type: doc
    ref: internal/exec/pipeline_legs_close.go:186
  - type: doc
    ref: internal/reconcile/auto_close.go:130
  - type: doc
    ref: internal/reconcile/auto_close.go:133
  - type: doc
    ref: internal/exec/pipeline_legs_flatten.go:64-103
---

## 事实/症状

D-188 地基审计发现：平仓流程中 swap 和 commission 现金流写入的错误被 `_ =` 忽略，且 LEGGED 自动收平拿到组件后未写入：

- `pipeline_legs_close.go:182`：`_ = e.store.InsertCashflow(ctx, ..., "SWAP", ...)` — 平仓 swap 现金流
- `pipeline_legs_close.go:186`：`_ = e.store.InsertCashflow(ctx, ..., "COMMISSION", ...)` — 平仓 commission 现金流
- `reconcile/auto_close.go:130`：reconcile 补写 swap 现金流
- `reconcile/auto_close.go:133`：reconcile 补写 commission 现金流
- `pipeline_legs_flatten.go:64-103`：LEGGED 自动收平拿到 `Fill.Swap/Commission`，但只写 CLOSE leg，不写现金流

`positions.realized_pnl` 是价格差 PnL；`cashflows` 表承载 swap/commission/funding 组件，`settlement.Settle` 用 `realized_pnl + SumCashflowsNet` 得到净值。现金流写入失败会使净值、结算、策略统计和复盘缺组件，且**不会报错**——系统看起来正常平仓了，但净值数字不完整。

## 根因或策略解释

平仓流程是异步多步骤的：ClaimClose → 执行平仓 → 计算盈亏 → 写入 cashflows → ClosePosition。在"计算盈亏 → 写入 cashflows"这一步，代码用 `_ =` 忽略了写入错误，可能是为了"不因日志写入失败阻塞平仓流程"。

但 cashflows 不是日志——它是净值账本的核心组件。忽略写入错误导致：
1. `settlement.Settle` 合成的净值少计 swap/commission → 结算与复盘统计错误
2. 策略淘汰评估基于错误净值 → 可能误淘汰/误保留策略
3. `DailyRealizedPnl` 当前只汇总 `realized_pnl`，因此它与 cashflow 组件也存在口径分离；不能把它描述成已覆盖完整净值

## 处置

**待修复（D-189 P0-2）**：现金流幂等查询和 `InsertCashflow` 错误显式日志+critical alert；现金流不完整保持 `CLOSING`，不按缺失组件写 `CLOSED`/进入正式结算。reconcile 收尾统一转给 Pipeline `ResumeClose`，不保留 raw cashflow 旁路。

**区分**：
- 核心数据写入（cashflows/positions/autotrade_state）→ 必须处理错误
- 审计日志写入（autotrade_logs/alerts）→ 不阻塞核心流程，但写入错误必须 log

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -n '_ = .*InsertCashflow' internal/`
- result: 4 处 `_ =` 忽略，另有 1 条 LEGGED 自动收平路径拿到 Fill 但未写现金流
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认 cashflows 表是否有其他写入路径兜底（如 settlement 事后补写）
- falsifies_when: 如果 settlement.Settle 会在后续 tick 中补写所有缺失 cashflows，则平仓时忽略错误可接受（有兜底）

## 适用边界

仅适用于 `InsertCashflow` 的 swap/commission 类型。其他 cashflow 类型（如价差盈亏）可能有不同写入路径。
