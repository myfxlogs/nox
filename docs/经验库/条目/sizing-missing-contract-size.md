---
schema_version: 2
id: sizing-missing-contract-size
kind: pitfall
risk: high
status: verified
keywords: [sizing, contract size, CS, notional, qty, CROSS_VENUE, MT, CFD, 漏乘, 敞口放大, oil, USOIL, XAU]
scope: [autotrade, CROSS_VENUE, PERP_FUNDING, MT5, MT4, CFD venue]
summary: qty=notional/price 漏乘 contract size → MT/CFD 腿敞口放大 CS 倍（油 CS=1000 → 1000×，$1K 预算变 $1M 敞口）；CEX 腿 CS=1 不受影响。修法=MT 腿查 CS fail-closed，qty=notional/(price×CS)。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-134
  - type: decision
    ref: D-135
  - type: commit
    ref: a84c92e
  - type: doc
    ref: internal/autotrade/engine_opp.go:104
  - type: server_fact
    source: postgres
    query: SELECT canonical, venue_buy, qty FROM positions WHERE type='CROSS_VENUE' AND canonical IN ('BRENT','WTI','USOIL') ORDER BY opened_at DESC LIMIT 5
    observed_at: 2026-08-28T00:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

D-134 油类 CROSS_VENUE 真损事故：`engine_opp.go:62` `qty := NotionalUSD / opp.PriceBuy` 漏乘 contract size。
USOIL price=82.66, CS=1000 → qty=12.09 手（应为 0.0121），名义敞口 $1M（预算 $1K 的 1000 倍）。
277/591 两账户合计 -$2,410 demo 真损。金银（XAU=100/XAG=1000-5000）同雷未除。

## 根因或策略解释

CROSS_VENUE sizing 公式 `qty = NotionalUSD / PriceBuy` 只对 CEX 腿正确（CS=1，qty=基础币数）。
MT/CFD 腿 qty 单位=手数，1 手名义 = CS × price，正确公式 = `NotionalUSD / (PriceBuy × CS)`。
漏乘 CS → qty 放大 CS 倍 → 敞口 = NotionalUSD × CS（油 1000×、金 100×、银 1000-5000×）。

与 `okx-perp-ctval-qty-conversion`（张数 vs 基础币）和 `quote-currency-notional-mismatch`（计价币≠USD）同族但根因不同——
本条是"qty 计算漏乘 venue 合约规格"，彼两条分别是"单位换算"和"计价币换算"。

## 处置

D-135 修复（a84c92e）：
- 提取 `computeBuyQty` 纯函数：MT 腿（`:mt5`/`:mt4`）查 CS fail-closed（查不到/未接线→skip），CEX 腿 CS=1 不变。
- 复用 D-062 注入链 `Service.OppContractSize`（透传 `oppContractSize` resolver，nil→false）。
- qty = `NotionalUSD / (PriceBuy × CS)`，MT 腿 CS 未知 → log + skip（fail-closed，禁默认值猜测）。
- 五用例先红(12.09)后绿(0.0121)验证。

## 证据

- check: 单测 `TestComputeBuyQty` 用例 1（USOIL price=82.66 CS=1000）
- result: 旧代码 qty=12.0977（红）→ 修后 qty=0.012098（绿）
- ref: D-134 / D-135 / commit a84c92e / internal/autotrade/engine_opp.go:104

## 反证条件

- check: CEX 腿（binance:perp/okx:perp）qty 行为是否改变
- falsifies_when: CEX 腿 qty ≠ NotionalUSD/price（CS=1 不变是回归红线，7 仓依赖）

## 适用边界

- MT/CFD venue（`:mt5`/`:mt4` 后缀）必须查 CS；CEX venue CS=1 恒成立。
- MT 品种 symbol_mappings 覆盖不全的会被 fail-closed 门拦下（保守正确，映射不全先补数据再交易）。
- SWAPARB sizing 不受影响（usdPerLot 已含 CS，D-135 §2 边界）。
- `service_execute.go:136` 腿间换算（csBuy/csSell 比值）已正确，不动。
