---
schema_version: 2
id: stale-data-propagates-wrong-judgments
kind: invariant
risk: high
status: verified
keywords: [数据时效, 轮询延迟, 事件驱动, 下游计算, 错误判断, funding, income, 账本]
scope: [采集层, 消费链, 全 venue]
summary: 交易数据延迟会沿整条计算链传播——源头晚一拍，下游全部算错、判断跟着错；时效性不是性能指标是正确性指标。账本类数据例外（权威源=REST 历史端点，轮询可重放补漏）。
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-450
  - type: decision
    ref: D-365
  - type: doc
    ref: cmd/core/wire_income.go:73
---

## 事实/症状

1. 2026-09-24 业主令：「交易数据不及时，会造成后面所有计算的延误，从而产生错误的数据，也就会出错误的判断」——时效性按正确性对待，不按性能对待。
2. 同族先例：D-079 funding 年化用「距结算剩余时间」当间隔 → 结算前年化爆炸 94.9%（真值 10.95%）——上游字段语义取错，下游展示直接错 8.7×。
3. D-450（当日业主令「全部要走事件驱动」）：gate/binance 交易事件面（订单/持仓/余额）上私有 WS 经 cex-proxy——交易类数据流化已完成。

## 根因或策略解释

计算链是串行的：采集 → 事实 → 判断 → 执行。源头数据晚一拍，下游每个环节都基于过期状态做决策——不是「结果晚到」，是「结果算错」。尤其对风控/结算/淘汰判，错误数据比缺失数据更危险（缺失会空态暴露，错误会静默决策）。

## 处置

- **交易类数据（订单/持仓/余额/tick）一律事件流为主源**——延迟=错误，轮询只作断连兜底双轨（D-450 已成文）。
- **账本类数据（income/进出账/结算记录）例外**：venue WS 不推逐 symbol 账本条目，权威源=REST 历史端点，轮询可重放+水位补漏，不违本条（D-365 裁定在案）。可用流事件当触发器加速拉取（okx `balance_and_position` eventType=funding_fee → 立即拉 bills），账本权威源不变。
- **审计动作**：评估任何「延迟可忍」声明时，先问下游消费链长度——链越长，延迟的正确性代价越大。
