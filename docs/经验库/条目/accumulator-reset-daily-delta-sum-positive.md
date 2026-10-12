---
schema_version: 2
id: accumulator-reset-daily-delta-sum-positive
kind: pitfall
risk: medium
status: observed
keywords: [累积计数器, 日增量, end−start, 清零重置, 负数, Σ正增量, allowance, counter reset, 快照差分, 应计口径]
scope: [累积计数器指标的周期聚合, 监控/记账 rollup, TRON allowance, 任何 cumulative counter 带重置]
summary: 对"累积计数器"取周期增量用朴素 end−start，在计数器清零重置的周期得出大负数；且重置事件若原始记录无金额（如 TRON WithdrawBalanceContract 无金额参数），事后从事件侧补不回。修法=对高频快照逐对求正增量之和 Σ max(0,Δ)——自然累积是正步进、清零是唯一负跳变，正增量求和恰好剥离重置（Prometheus rate() 处理 counter reset 同思想）。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: docs/D-211设计文档审计报告-2026-09-03.md §1 F2
  - type: doc
    ref: docs/TRX质押-P0侦察报告-2026-09-03.md §4.1
  - type: decision
    ref: D-211
---

## 事实/症状

D-211 设计把投票收益定义为"当日 allowance 增量"。allowance 是自上次领取以来的累积值，业主每 1~3 周执行一次 claim（链上 21 笔 WithdrawBalanceContract 实证）清零重启：claim 日 allowance 从 ~7 TRX 归零再重新累积，朴素 `end−start` 得出大负数；且 WithdrawBalanceContract 无金额参数，不能从交易记录还原领取额来修正。负面连锁：周期报表出现负收入 → 触发人工纠错 → 若叠加判别列失真（见 upsert-writer-must-stamp-new-discriminator-column）则纠错再被自动任务吞掉。

## 根因或策略解释

累积计数器（allowance、各类 metrics counter、里程表）的语义是"自最近一次重置起累积"，跨重置边界的差分无意义。高频快照下增量只有两种：正步进（自然累积）与负跳变（重置）；Σ max(0,Δ) 保留前者剥离后者，数学上等于"周期内真实累积"。快照频率决定粒度上限（D-210 实测 allowance 按维护期窗口量子化更新，5min 快照已远细于其步进）。

## 处置

rollup 改为：`period_income = Σ max(0, Δvalue)` 对周期内相邻快照逐对求和；首周期（无前日末样本）以首个样本为基线（少量低估，可接受并注记）；快照断档（停机）时断档段增量归入恢复后所在周期（漂移注记）。重置事件本身单独入账为"内部转移"而非收入（应计口径：收入在累积时确认，重置/提取只是形态转换）。

## 证据

- check: D-210 链上实测——21 笔 claim 与 latest_withdraw_time 同刻吻合（allowance=自上次领取累积）；claim 日 end−start 必为负（清零实证）；合约无金额参数（raw_data 核对）
- result: Σ正增量规则在 claim 日给出正确应计（+0.5+0.2=0.7，朴素口径=-6.8）
- ref: D-211 审计 F2（43272ce）

## 反证条件

- check: 施工落地后对真实 claim 日的 rollup 输出与手工对账
- falsifies_when: 存在非重置原因的负 Δ（如链上回滚/字段语义变化），Σ正增量会漏记——届时须引入重置事件显式对账

## 适用边界

累积计数器带清零重置的周期聚合；要求快照频率 ≥ 累积步进频率（否则步进混入相邻周期，量级可控）；若计数器从不重置，朴素 end−start 与 Σ正增量等价，用哪个都行（朴素更省）。
