---
schema_version: 2
id: compute-buy-qty-missing-jpy-conversion
kind: pitfall
risk: high
status: observed
keywords: [computeBuyQty, JPY 换算, USD notional, qty, swap deviation, brokerActualNetSwapBps, CROSS_VENUE, MEANREVERT, USDPerLotForLeg]
scope: [autotrade, dashboard, CROSS_VENUE, MEANREVERT, swap 偏差与退出, JPY 计价品种]
summary: USD notional 换算分叉：computeBuyQty/MEANREVERT 与自动和手动 swap 偏差门、broker 实际 swap bps 仍缺 JPY→USD；同一价格事实多套口径，可能造成 qty、偏差和退出量纲错误。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/engine_opp.go:136
  - type: doc
    ref: internal/dashboard/swap_notional.go:77
  - type: doc
    ref: internal/dashboard/service_execute_internal.go:144-158
  - type: doc
    ref: internal/autotrade/gate_checks.go:133-149
  - type: doc
    ref: internal/autotrade/engine_position.go:206-247
  - type: doc
    ref: internal/dashboard/service_swap_deviation.go:103-118
---

## 事实/症状

D-188 地基审计发现 notional/qty/偏差计算有多套口径：

1. **SWAPARB 路径**（`swap_notional.go:77` `USDPerLotForLeg`）：`CS × price` + JPY 对 ÷ `USDJPY` + 商品白名单 — **有币种换算**
2. **CROSS_VENUE/MEANREVERT 路径**（`engine_opp.go:136` `computeBuyQty`）：`notionalUSD / (PriceBuy × CS)` — **无币种换算**
3. **ExecuteMeanRevertWithCap**（`service_execute_internal.go:144-158`）：`qty × CS × price` — **无 JPY 换算**
4. **自动 swap 偏差门**（`autotrade/gate_checks.go:133-149`）与**手动 swap 偏差门**（`dashboard/service_swap_deviation.go:103-118`）：facts USD 换算分母仍是 JPY 口径。
5. **broker 实际 swap bps**（`autotrade/engine_position.go:206-247`）：实际 USD 仍除以未换算的 JPY 名义。

**后果**：AUDJPY 等 JPY 计价品种走 CROSS_VENUE/MEANREVERT 时，`PriceBuy` 是 JPY 价格（如 95.0），`notionalUSD / (95.0 × 1000)` = qty，但正确应该是 `notionalUSD / (95.0 × 1000 / USDJPY)`。qty 会错约 150 倍（1/USDJPY）。

## 根因或策略解释

SWAPARB 路径在 D-072 设计时就考虑了币种换算（`USDPerLotForLeg`）。CROSS_VENUE/MEANREVERT 路径是早期代码，假设所有品种都是 USD 计价，没有处理 JPY 计价品种。

qty、偏差和实际 swap bps 逻辑独立演化，没有统一到同一 USD notional helper。

## 处置

**待修复（D-190 P1-1）**：提取一个唯一的 `notionalPerLotUSD` resolver，覆盖 `computeBuyQty`、`ExecuteMeanRevertWithCap`、自动/手动 swap 偏差门和 `brokerActualNetSwapBps`；JPY 统一除 `USDJPY`，FX 缺失 fail-closed，所有单测使用具体 JPY 数值锚。

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -n 'computeBuyQty\|USDPerLotForLeg\|notionalPerLot' internal/autotrade/ internal/dashboard/`
- result: SWAPARB 主 sizing 已换算；CROSS_VENUE/MEANREVERT、自动/手动 swap 偏差与 broker 实际 bps 路径仍有未换算口径
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 逐一确认所有受影响路径是否可能接收 JPY canonical，并用 USDJPY 复算 qty、偏差与实际 bps
- falsifies_when: 如果所有受影响入口都在进入计算前拒绝 JPY canonical，且不存在 JPY 计价持仓，则 JPY 量纲缺陷不影响运行范围；否则本条目成立

## 适用边界

仅影响 JPY 计价品种（AUDJPY/GBPJPY/EURJPY 等）进入上述 qty、swap 偏差或实际 bps 计算路径的场景。USD 计价品种不受此 JPY 换算缺陷影响。
