---
schema_version: 2
id: settlement-skip-rule-vs-calibrator-venue-coverage
kind: pitfall
risk: high
status: verified
keywords: [settlement, pnl_estimated, calibratePnL, PnLCalibrator, skipped, strategy_stats, 自动淘汰断环, CEX 腿, 覆盖面不匹配, fee_usd 缺账]
scope: [exec, settlement, 含 CEX 腿的任何持仓类型, 淘汰环]
summary: 「校准不全则结算不入样本」规则的覆盖面=校准器覆盖的 venue；校准器只实现 MT 时，任何含 CEX 腿的仓永久 partial→pnl_estimated→settlement skip→strategy_stats 零样本=自动淘汰对该策略空转（XASSET 17/17 skipped、strategy_stats 无该策略行）。规则设计时须与其依赖组件的 venue 覆盖面对齐；覆盖不到的腿以成交回执为权威而非判 partial。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [事实核验, KB维护]
evidence_refs:
  - type: doc
    ref: docs/开平仓管线逻辑审计-2026-09-23-Devin.md §1 P1-4
  - type: doc
    ref: internal/exec/pnl_calc.go:98-131（calibratePnL shouldCalibrate 计全部有 OrderID 的 OPEN FILLED 腿）
  - type: doc
    ref: internal/settlement/settlement.go:128（strings.Contains(p.Note,"pnl_estimated") → skip）
  - type: decision
    ref: D-159
  - type: server_fact
    source: postgres
    query: select completeness_status, pnl_quality, count(*) from position_settlements where position_id between 484 and 500 group by 1,2
    observed_at: 2026-09-23T12:40:00Z
    server_revision: "master eee7a40e"
    response_hash: ""
---

## 事实/症状

- XASSET_CARRY 17 单 CLOSED：position_settlements 全部 `skipped/unknown`（484-487 expected_usd<=0；488-500 `pnl_estimated: partial or no calibration`）；strategy_stats 表无 XASSET_CARRY 行；Σrealized≈−$40 而淘汰判定一次未跑。
- 同批 17 单 position_legs.fee_usd 全 0/NULL、commission cashflow 0 行——CEX 一进一出≈$4.3/单（与典型 capture 同量级）账本不知。

## 根因或策略解释

D-159 W3a R4「estimated 不入正式统计」为 MT-only SWAPARB 设计（历史单校准可得）。`calibratePnL` 的 shouldCalibrate 计**全部**有 OrderID 的 OPEN FILLED 腿，但 PnLCalibrator 只有 MT 实现 → 含 CEX 腿的仓 `calibrated != shouldCalibrate` 恒成立 → note=pnl_estimated → SettleAt skip。规则与其依赖组件（校准器）的 venue 覆盖面不对齐=对新策略族永久绕过淘汰环。费用侧同型：D-391 只在 OPEN 腿采集 FeeUSD，三处 CLOSE 腿构造与 writeCloseCashflows 均不消费该字段（采集落腿即丢）。

## 处置

- shouldCalibrate 只计校准器覆盖的 venue（MT）；CEX 腿成交回执即权威价，不算 partial；纯 CEX 仓不写 estimated。
- strategy_stats 补缺行；24 行 skipped 重置后 SettleAt 幂等重跑。
- CLOSE 腿写 FeeUSD + commission cashflow（actual）单源入账。派单=D-422。

## 证据

- check: 上述 SQL + `select strategy from strategy_stats`
- result: 17/17 skipped；strategy_stats 仅 CARRY/CROSS_VENUE/MEANREVERT/PERP_FUNDING/SWAPARB 五行
- check: `grep -rn FeeUSD internal/exec/pipeline_resume*.go internal/exec/pipeline_legs_flatten.go internal/exec/pipeline_cashflow.go`
- result: 零引用
- ref: docs/开平仓管线逻辑审计-2026-09-23-Devin.md §1 P1-4；D-422

## 反证条件

- check: D-422 部署后重结算 498，看 completeness=complete、strategy_stats 出现 XASSET_CARRY total_trades=17
- falsifies_when: 重结算仍 skipped（=另有 skip 分支，如 expected_usd<=0 对短持仓的口径问题）

## 适用边界

- 一切「质量门规则」（不入样本/不决策/fail-closed）落地前必须核对：规则判据依赖的组件对当前全部 venue/资产族的覆盖面；覆盖不到=规则退化为永久绕过，且绕过是静默的（skip 行看似正常）。
- 采集字段落库后必须 grep 全部消费点：declared-but-never-consumed（fee_usd）与 declared-but-never-wired（D-391 前形态）同族。
