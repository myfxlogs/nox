---
schema_version: 2
id: mt5-spec-vs-settlement-disconnect
kind: pitfall
risk: high
status: verified
keywords: [MT5, Exness, trial, demo, swap, 结算, 规格配置, SymbolParams, OpenedOrders, OrderHistory, 预期偏保守]
scope: [adapter, mt5, swapledger, evaluator, Exness]
summary: MT5「规格配置了 swap」≠「实际结算」——Exness trial 持仓+历史单双 API 无 swap；预期用规格值（真盘会结算）demo 实测偏高，结算对账兜底
first_seen: 2026-08-24
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: server_fact
    source: mt5grpc3.mtapi.io
    query: OpenedOrders（277842155 AUDJPYm SELL 2355655056，无 Swap 键）+ OrderHistory（8-20~8-22 80 行 swap 全 None）
    observed_at: 2026-08-24
  - type: server_fact
    source: arb-core ListBrokerPositions
    query: 52993526 AUDJPY swap=4.99（对照：IC demo 正常日结）vs 277842155 无 swap 字段
    observed_at: 2026-08-24
  - type: doc
    ref: internal/reconcile/reconcile_queriers.go:242（VenueOrder.Swap=ord.GetSwap() 直取）
---

## 事实/症状

Exness-MT5Trial5（试用服务器）上，SymbolParams 规格**配置了** swap（AUDJPYm 买入 -0.2/卖出 -2.2 点，facts 采集一致），但：

1. 持仓对象（OpenedOrders 直连网关）：**无 Swap 字段**（其余字段 Profit/Volume/OpenTime 齐全）；
2. 历史订单（OrderHistory 8-20~8-22 全 80 行）：**swap 全 None**；
3. 对照组 IC（ICMarketsSC-Demo）：持仓 swap=4.99 USD 正常日结。

→ 结论：**Exness trial 服务器不执行 swap 结算**（或结算值不向 API 暴露），规格规则存在但结算不生效。此场景下持仓期 swap 实际=0（正向持有者少付、反向持有者少收），实际收益与规格预期出现系统性偏差。

## 根因或策略解释

- MT5「规格（SymbolParams 的 swap 字段）」与「结算行为」是**服务器侧解耦的**：broker 可配置规则但不执行（trial/demo 简化），API 按服务器实际结算值透传。
- 我方预期计算（evaluator/swap 组合扫描）用**规格值**——对真盘账户正确，对不结算的 trial/demo 账户偏保守（少付方向=实际更好，多收方向=实际更差——本案例 #264 是 Exness 空腿少付 → 实际比预期好）。
- 教训（同 [[business-assumptions-in-code-audit]] 同族）：**「服务器配置了」≠「服务器会执行」**——结算类事实必须实测（持仓/历史单/平仓对账），规格只作预期输入。

## 处置

- **预期计算继续用规格值**（真盘会结算，规格=真实成本），不做修正——实测偏差由结算对账（settlement）兜底。
- 评估 demo/trial 账户的收益时标注「结算未生效」风险：方向判断先看哪条腿少付/少收。
- 接入新 broker 账户时，先查其持仓 swap 字段是否随日结变化（一条 OpenedOrders 即证，见 [[mtapi-gateway-query-procedure]]）。
- 平仓终验：#264 平仓时 CloseOrder 的 Fill.Swap（exec.go:35 透传网关权威值）最后确认。

## 证据

- check: 本会话三连取证（直连网关 OpenedOrders/OrderHistory + core RPC ListBrokerPositions 对照）
- result: Exness 双 API 无 swap；IC 同日有 swap=4.99；规格与结算解耦成立
- ref: 会话 2026-08-24（#264 Exness 腿 swap 定案）

## 反证条件

- check: #264 平仓时 Exness 腿 CloseOrder 返回 Fill.Swap≠0，或 Exness 真盘账户持仓 swap 日结
- falsifies_when: 平仓单出现 swap → 「trial 不结算」推翻，改为「结算延迟/仅平仓时结算」（预期仍准，偏差=时机）

## 适用边界

- 仅 Exness-MT5Trial5 实测（2026-08-24）；其他 broker 的 trial/demo 服务器**各自实测**（ICMarketsSC-Demo 对照=正常结算，证明 demo≠不结算，是 broker/服务器配置差异）。
- 不适用于真盘账户（真盘必结算，规格=成本）。
