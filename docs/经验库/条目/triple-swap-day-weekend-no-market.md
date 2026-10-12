---
schema_version: 2
id: triple-swap-day-weekend-no-market
kind: pitfall
risk: high
status: verified
keywords: [triple_swap_day, Sunday, Saturday, 周末不开盘, 三倍日, swapledger, settleMultiplier, 市场日历, 规格语义]
scope: [swapledger, adapter, mt5, mt4, SWAPARB, CARRY]
summary: MT5 规格 triple_swap_day=Sunday/Saturday 时代码字面记3x，但外汇周末不开盘→三倍日不生效→swapledger 虚高3倍；规格字段值需结合市场日历解释不能字面应用
first_seen: 2026-08-26
last_verified: 2026-09-26
skills: [生产取证, KB维护]
evidence_refs:
  - type: server_fact
    source: postgres
    query: SELECT kind, venue, symbol, value FROM facts WHERE kind='triple_swap_day' ORDER BY ts DESC LIMIT 10
    observed_at: 2026-08-26
  - type: server_fact
    source: rpc
    query: ListBrokerPositions #264 swap 累计值（D-102 实证 IC 结算逐日 1x/周末 0）
    observed_at: 2026-08-25
  - type: decision
    ref: D-110
  - type: commit
    ref: 8671324
  - type: doc
    ref: internal/swapledger/ledger_helpers.go:55-72
---

## 事实/症状

MT5 网关采集 `SymGroup.ThreeDaysSwap` 写入 facts 表 `triple_swap_day`，全 venue（52993526/277842155/REDACTED_MT_ACCT/39050818/1314615）137 条记录值全=0（Sunday）。swapledger `settleMultiplier` 字面应用：`day.Weekday() == tripleDay → multiplier=3`，周日记3x。

但外汇市场**周日不开盘**——三倍日=Sunday 实际不生效。D-102 实证：IC 52993526 持仓 swap 累计值（RPC `BrokerPosition.swap`）逐日 1x、周末 0，无任何3倍日。

→ #264 swapledger cashflows 8-23（周日）记 +14.52（≈3×4.84），实际应为 +4.84，**虚高 +9.68**。

## 根因或策略解释

MT5 规格字段 `ThreeDaysSwap` 的值是**枚举星期几**（0=Sunday...6=Saturday），语义是"该日过夜收3倍 swap"。但外汇市场周六/周日不开盘，broker 将三倍日设为 Sunday 实质等于"无三倍日"（或"周末过夜收0"）。

代码只做了字面比较（`day.Weekday() == tripleDay`），没结合**市场日历语义**（周末不开盘→三倍日不生效）。这是 `[[business-assumptions-in-code-audit]]` 的子类：知道了规格值（Sunday）后，对规格值的**语义解释**仍出错——不是"假设未验证"（已验证=Sunday），而是"验证后的值的应用方式错误"。

与 `[[mt5-spec-vs-settlement-disconnect]]` 区别：后者是"规格配置了≠实际结算"（broker 不执行），本条目是"规格值=Sunday 但 Sunday 在外汇=不开盘"（值正确但语义需领域知识解释）。

## 处置

D-110 P1 修复 `settleMultiplier`：`tripleDay == Sunday || tripleDay == Saturday → return 1`（周末不开盘不收3倍）。fail-closed 语义保留（未知三倍日仍返回0跳过记账）。

**泛化教训**：规格字段值是枚举/常量时，必须结合领域语义解释：
- 三倍日=周末 → 不生效（外汇/CFD 周末闭市）
- 三倍日=周三 → 正常3x（工作日过夜）
- 采集层只读规格值，应用层（记账/预期计算）负责语义解释

## 证据

- check: facts 表 triple_swap_day 全 venue value=0（Sunday），137 条
- check: D-102 实证 IC 52993526 BrokerPosition.swap 逐日 1x/周末 0（RPC ListBrokerPositions）
- check: #264 swapledger cashflows 8-23（周日）+14.52 ≈ 3×4.84（虚高3倍）
- result: settleMultiplier 修复后 Sunday/Saturday → 1（7 case 单测全绿）
- ref: D-110 / commit 8671324 / internal/swapledger/ledger_helpers.go:55-72

## 反证条件

- check: 某 broker 周日开盘交易（如加密/7×24 品种）且 triple_swap_day=Sunday 实际收3倍
- falsifies_when: 周日过夜持仓 swap 累计值跳3x → "周末不开盘"前提失效，需按品种交易时段分型

## 适用边界

- 外汇/CFD 周末闭市品种（AUDJPY 等）——三倍日=周末不生效
- 加密 7×24 品种不适用（周末有交易，三倍日=周末可能生效）
- MT4 的 `SwapRollover3Days` 同理（0=Monday...4=Friday，无周末值，不受影响）
