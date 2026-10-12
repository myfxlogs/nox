---
schema_version: 2
id: evaluator-roundtrip-cost-blindspot
kind: pitfall
risk: high
status: verified
keywords: [round-trip成本, 开仓平仓, evaluator, net_bps虚高, 假机会, 摩擦费, 入库门槛, CROSS_VENUE, FUNDING_HEDGE, 垃圾机会, bid-ask穿透, half-spread单位, D-113]
scope: [evaluator, engine, detector, CROSS_VENUE, FUNDING_HEDGE, CARRY, PERP_FUNDING, 全策略]
summary: 评估器 net_bps 只算开仓成本 → 净收益虚高假机会入库；D-078 修 commission+slippage×2 但 bid-ask 穿透仍漏算；D-113 补 roundTripSpreadBps=half-spread×4/mid×10000；实证 51 笔只 1 盈累计 -$512
first_seen: 2026-08-22
last_verified: 2026-09-26
skills: [事实核验]
evidence_refs:
  - type: server_fact
    source: postgres
    query: SELECT p.type, count(*), round(avg(o.gross_bps)::numeric,1), round(avg(o.net_bps)::numeric,1), round(avg(p.realized_pnl)::numeric,2) FROM opportunities o JOIN positions p ON p.opportunity_id=o.id WHERE p.status='CLOSED' AND p.realized_pnl IS NOT NULL GROUP BY p.type
    observed_at: 2026-08-22T02:05:44Z
    server_revision: ""
    response_hash: ""
  - type: server_fact
    source: postgres
    query: SELECT type, count(*) FILTER (WHERE net_bps < 5) FROM opportunities WHERE expires_at <= now() GROUP BY type
    observed_at: 2026-08-22T02:15:34Z
    server_revision: ""
    response_hash: ""
  - type: commit
    ref: "D-078 commit"
  - type: doc
    ref: internal/evaluator/evaluator.go:60-87
  - type: decision
    ref: D-113
  - type: commit
    ref: 43346cc
  - type: server_fact
    source: postgres
    query: SELECT count(*) FILTER (WHERE realized_pnl > 0), count(*) FILTER (WHERE realized_pnl <= 0), sum(realized_pnl) FROM positions WHERE type='CROSS_VENUE' AND status='CLOSED'
    observed_at: 2026-08-26T12:30:00Z
    server_revision: "D-113 部署前"
    response_hash: "51 笔: 1 盈 / 21 亏 / 累计 -$512.24"
---

## 事实/症状

业主发现机会面板大量 0.几 bps 收益的「机会」，质疑连摩擦费都不够。

DB 实证：69297 条机会中 47% (32446条) net < 5bps，其中 8240 条 < 1bps。已平仓 31 笔 CROSS_VENUE：avg gross 46.1 bps，avg net_pred 43.5 bps，但 avg realized = **-$1.19**（7 胜 24 负）。18 笔 FUNDING_HEDGE：avg gross 10.5%，avg realized = **-$3.14**。

## 根因或策略解释

**评估器只算开仓成本，不算平仓成本**：

```
旧 CROSS_VENUE: net = gross - commission - slippage        （只扣开仓）
旧 息差型:      oneTimeCost = spread + commission + slip    （只算开仓 breakeven）
```

实际 round-trip 需要开仓+平仓各付一轮 commission+slippage（+spread for 息差型）。实证 CROSS_VENUE 平仓滑点 avg 3.8bps（模型假设 2.0），因为价差不收敛甚至发散。

**真实 round-trip 成本**（从已平仓交易实证）：
- CROSS_VENUE CFD：开仓 2.1bps + 平仓 5.4bps = 7.5bps + 基差风险 ~5bps → gross ≥ 20bps 才可能盈利
- FUNDING_HEDGE CEX：手续费 20bps/边×2 + 滑点 4bps×2 = 48bps → 需持仓 >27 天（6.45%年化时）
- SWAPARB MT5：无佣金，spread 2.4 + slip 1.0 + 基差 5 = 8.4bps → 需持仓 >17 天

## 处置

1. **evaluator.go round-trip 修正**（D-078）：
   - CROSS_VENUE: `net = gross - (commission + slippage) * 2`
   - 息差型: `oneTimeCost = (spread + commission + slippage) * 2`（breakeven_days 翻倍）
2. **engine.go 入库门槛**：CROSS_VENUE net < `ARB_STORE_MIN_NET_BPS`（默认 5bps）不入库
3. **检测器门槛上调**：`ARB_CROSS_MIN_GROSS_BPS_CFD` 默认 0.1 → 20
4. **DB 清理**：DELETE 31423 条已过期垃圾机会 + VACUUM
5. **D-113 往返 bid-ask 穿透修正**（2026-08-26 续篇）：
   - D-078 只修了 commission+slippage×2，**bid-ask 穿透仍漏算**——D-035 注释"gross 已含双边点差"只对开仓方向成立（detector 用 buy.Ask/sell.Bid），平仓方向要吃对侧全宽 spread
   - 修正：`roundTripSpreadBps = (spreadBuy + spreadSell) / mid * 10000 * 4`（half-spread × 4 = 开仓+平仓各吃全宽 spread）
   - 实证：51 笔只 1 盈累计 -$512；XPDUSD 宽 spread venue 组合 net 21.27 → 修正后 -65.6 → trap → 不入库
   - **单位坑**：spreadBuy/spreadSell 是原始价格单位 half-spread（美元 for USD 标价），EURUSD ≈ 0.0001（1 pip）不是 0.5（45% of price 荒谬）；旧测试用 0.5 从未出错因旧分支忽略 spread 参数，D-113 启用后才暴露
   - 运行时实证：部署后删 111 条旧机会，60s 后 0 条新入库（net 为负被 netStoreGate 过滤）

## 证据
- 2026-08-23 重现: 门槛语义与展示口径不一致三连——D-084 基准当收益（同所/跨所 spot+perp）、D-085 门槛在毛不在净（PERP_FUNDING 净 0.099%/回本 1519 天仍亮灯）——通则：**门槛必须作用在与卡片显示完全同口径的净上（毛−基准−全部摩擦）** | ref: D-084/D-085 + 业主三次实证抓出

- check: 31 笔 CLOSED CROSS_VENUE → avg gross 46.1bps, avg realized -$1.19
- check: 平仓滑点实证 avg 3.8bps（模型假设 2.0）→ 模型低估平仓成本
- check: 69297 条机会 47% < 5bps → 假机会污染
- check: 修复后 evaluator test TestEvaluateCrossVenueRoundTrip → gross=5bps 判 trap, gross=10bps net=4.8 grab
- result: DELETE 31423 rows, VACUUM 回收, 剩余 37948 条均 ≥5bps/≥4%年化
- ref: D-078, evaluator.go:60-87, engine.go:258-262, config.go:73-76

## 反证条件

- check: 部署后观察新入库机会 net_bps 分布——应全部 ≥5bps（CROSS_VENUE）或 ≥4%年化（息差型）
- falsifies_when: 新入库机会仍出现 <5bps → 入库门槛未生效（env 未透传 or 检测器产 gross>20 但 round-trip 后 net<5）
- check: 未来 CROSS_VENUE 平仓 realized_pnl 应趋近 net_bps 预测值
- falsifies_when: realized_pnl 仍系统性低于 net_bps → 还有未覆盖的成本项（如基差风险需量化入模）

## 适用边界

- round-trip ×2 适用于所有策略类型（开仓+平仓各付一轮摩擦）。
- 入库门槛 `ARB_STORE_MIN_NET_BPS` 仅适用于 CROSS_VENUE（net 为一次性 bps）；息差型 net 为年化百分点，由检测器 MinNetAnnualized 过滤。
- 实证数据来自 CFD 跨 broker（IC Markets/Exness）和 CEX（Binance/OKX/Gate）模拟盘，真实盘口摩擦可能不同。
- 基差风险 ~5bps 为 #264 AUDJPY 单样本估计，其他品种/broker 对的基差特征可能不同。
