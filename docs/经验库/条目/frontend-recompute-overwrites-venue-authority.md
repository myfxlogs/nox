---
schema_version: 2
id: frontend-recompute-overwrites-venue-authority
kind: pitfall
risk: high
status: verified
keywords: [前端重算, venue 权威, pnl, ContractSize, fx 换算, 零信任, 覆盖透传值]
scope: [dashboard, broker_position, service_broker]
summary: 后端"实时化"重算覆盖 venue 权威透传值，但重算公式缺 ContractSize+fx 换算 → JPY 计价符号 pnl 差 660 倍；venue 已换算账户货币的权威字段不应被重算覆盖（D-043 零信任）
first_seen: 2026-08-27
last_verified: 2026-08-27
skills: [事实核验]
evidence_refs:
  - type: rpc
    desc: "ListBrokerPositions #290 REDACTED_MT_ACCT 腿：修复前 pnl=0.003（F5 重算），修复后 pnl=2.413（MT5 透传）"
  - type: code
    desc: "service_broker.go brokerPositionToProto F5 重算 Volume×(mid−open)×dir 缺 CS×100000 + ÷USDJPY"
  - type: commit
    desc: "79ed128 引入 F5（2026-08-21）；4eb773a 移除 F5（2026-08-27 D-116）"
related:
  - d043-zero-trust-frontend-render-only
---

## 症状
#290 REDACTED_MT_ACCT 腿前端显示浮盈 $0.003，MT5 实际权威值 ≈$2.41，差 660 倍。

## 根因
79ed128（修复③F5）在 brokerPositionToProto 加"新鲜报价时 pnl 实时重算"：
`pb.Pnl = o.Volume * (mid - o.OpenPrice) * dir`
覆盖了 MT5 OpenedOrders 返回的 Order.Profit（已换算账户货币 USD）。

公式两个缺陷：
1. 缺 ContractSize：Volume 是手数（0.01），AUDJPY 1手=100,000，差 10万倍
2. 缺 fx 换算：AUDJPY 算出来是 JPY，需 ÷USDJPY 换 USD，差 ~150 倍
合计差 ~660 倍。

## 教训
- venue 权威字段（MT5 Order.Profit 已换算账户货币）不应被后端重算覆盖（D-043 零信任）
- "实时化"不等于"重算"——venue 每次 poll 拿最新值已是实时
- 重算公式必须完整：手数 × ContractSize × 价差 ÷ fx，缺任一即灾难性错误
- EURUSD 测试用例（USD 计价，CS=100000）恰好掩盖了 fx 缺失——测试需覆盖非 USD 计价符号

## 修复
D-116：删除 F5 重算，pnl 保持 venue 透传 Order.Profit。保留 currentPrice+fresh（前端显示现价用）。
