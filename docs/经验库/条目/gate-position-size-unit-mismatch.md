---
schema_version: 2
id: gate-position-size-unit-mismatch
kind: pitfall
risk: high
status: verified
keywords: [Gate, 持仓, size, 合约数, 基础币, quanto_multiplier, Volume, 单位不一致, OKX, Binance]
scope: [reconcile, cex_positions, CEXPositionQuerier, BrokerPosition, CloseBrokerPosition]
summary: Gate perp 持仓 size=合约数（284 张），但 OKX/Binance 持仓 Volume=基础币数量（2.84 ETH）；不转换会导致平仓时 gateContractSize 再除以 quanto → 284/0.01=28400 张（100 倍错误）→ 交易所拒绝
first_seen: 2026-08-27
last_verified: 2026-08-27
skills: [事实核验]
evidence_refs:
  - type: rpc
    desc: "CloseBrokerPosition gate:perp ETH_USDT → LIQUIDATE_IMMEDIATELY（size=28400 张 vs 持仓 284 张）"
  - type: code
    desc: "cex_positions.go gatePositions 加 gateQuantoMultiplier 查合约规格转基础币（合约数×quanto）"
  - type: log
    desc: "[gate-debug] body size=-28400（修复前）→ 修复后 Volume=2.84 ETH → size=-284 张 → CLOSED"
  - type: commit
    desc: "5e3bdde D-121 Gate 持仓 Volume 单位修复（2026-08-27）"
related:
  - d120-perp-funding-exec-chain
  - d121-cex-position-display
  - cex-close-reduceonly-missing
---

## 症状
gate:perp ETH_USDT 持仓 Volume=284（Broker 实持仓显示），补资金后平仓仍报
`LIQUIDATE_IMMEDIATELY`。debug 日志显示 body size=-28400（应为 -284）。

## 根因
三所持仓 API 返回的 size 单位不一致：
- OKX：`pos`=基础币数量（如 40 ETH）
- Binance：`positionAmt`=基础币数量（如 0.4 ETH）
- **Gate：`size`=合约数（如 284 张），需乘 quanto_multiplier 才是基础币**

CEXPositionQuerier 直接把 Gate `size` 当 Volume 透传 → BrokerPosition.Volume=284
（合约数，非基础币）。CloseBrokerPosition 把 284 当基础币传给 PlaceOrder →
gateContractSize 把 284 除以 quanto=0.01 → 28400 张（100 倍错误）。

## 教训
- CEX 三所持仓 API 的 size 单位不统一——必须在查询层统一转换为基础币
- Gate 的 size=合约数是 Gate 特有的 quanto 合约模型（每张合约=quanto_multiplier 个基础币）
- 下单时 gateContractSize 把基础币转回合约数（qty/quanto），持仓查询必须反向（合约数×quanto）
- 单位不一致的 bug 不会在"单所测试"中暴露——必须跨所对比 Volume 才能发现
- 100 倍错误是 quanto=0.01 的倒数——quanto 越小放大倍数越大

## 修复
D-121：gatePositions 查合约规格 `gateQuantoMultiplier`，Volume=合约数×quanto_multiplier，
与 OKX/Binance 单位对齐。per-contract 缓存避免重复请求，失败兜底 1.0（不阻断）。
