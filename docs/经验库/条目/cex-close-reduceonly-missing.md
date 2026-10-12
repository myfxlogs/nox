---
schema_version: 2
id: cex-close-reduceonly-missing
kind: pitfall
risk: high
status: verified
keywords: [CEX 平仓, reduceOnly, reduce_only, 保证金, gate, OKX, Binance, perp, PlaceOrder 反向减仓]
scope: [exec, cex_executor, CloseBrokerPosition, BrokerManager, pipeline_legs]
summary: CEX perp 平仓走 PlaceOrder 反向减仓时必须带 reduceOnly=true（OKX）/reduce_only=true（gate），否则交易所当新开反向仓要求保证金；三所对齐易遗漏——OKX/Binance 在 D-108/D-120 已修但 gate 漏了，且 CloseBrokerPosition RPC 路径也漏了
first_seen: 2026-08-27
last_verified: 2026-08-27
skills: [事实核验]
evidence_refs:
  - type: rpc
    desc: "CloseBrokerPosition gate:perp ETH_USDT → http 400 INSUFFICIENT_AVAILABLE（reduce_only 漏）→ 修复后 CLOSED"
  - type: code
    desc: "cex_executor_gate.go placeGate body 加 \"reduce_only\":%t（PositionSide!=\"\"=true）"
  - type: code
    desc: "broker_manager_orders.go CloseOrder CEX 路径设 PositionSide=原持仓侧（触发 reduce_only）"
  - type: commit
    desc: "c4574b2 D-121 gate reduce_only 修复（2026-08-27）"
related:
  - d120-perp-funding-exec-chain
  - d121-cex-position-display
---

## 症状
业主问"平仓还需要保证金？"——CloseBrokerPosition RPC 对 gate:perp 持仓报
`INSUFFICIENT_AVAILABLE`（保证金不足）。补资金后仍报 `LIQUIDATE_IMMEDIATELY`。

## 根因
CEX perp 平仓 = PlaceOrder 反向减仓（无 CloseOrder 概念）。反向单不带 reduceOnly
时，交易所当成**新开反向仓**（要保证金），而非减仓。

三所 reduceOnly 实现不一致且易遗漏：
- OKX：body `"reduceOnly":%t`（D-108 已修，PositionSide!=""=true）
- Binance：query `reduceOnly=true`（D-108 已修，one-way 模式 PositionSide!=""=true）
- **gate：body `"reduce_only":%t`（D-121 本次修复，之前漏了）**

另一个遗漏：`CloseBrokerPosition` RPC → `BrokerManager.CloseOrder` → `CEXExecutor.CloseOrder`
返回 `"cex: use PlaceOrder with opposite side for close"`（占位错误）。CEX 平仓路径
只在 `pipeline_legs.go ClosePosition` 中实现（有 PositionSide），RPC 路径没接上。

## 教训
- CEX 平仓的 reduceOnly 是三所对齐的**完整集合**——修一所不够，必须检查所有所
- CloseOrder 接口的 CEX 占位实现（返回 error）会导致 RPC 平仓路径不通——
  占位 error 必须有替代路径（BrokerManager.CloseOrder 对 CEX 走 PlaceOrder）
- PositionSide 是 reduceOnly 的触发条件：空=开仓不带，非空=平仓带（三所统一口径）
- 业主问"为什么需要保证金"是高价值信号——平仓不应要保证金，根因一定是 reduceOnly 漏

## 修复
D-121：① gate placeGate body 加 `"reduce_only":%t`；② BrokerManager.CloseOrder
对 CEX venue 从 bpm 快照查 side/volume/symbol → PlaceOrder 反向减仓 + 设 PositionSide。
