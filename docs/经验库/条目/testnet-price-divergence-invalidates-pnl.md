---
schema_version: 2
id: testnet-price-divergence-invalidates-pnl
kind: pitfall
risk: medium
status: verified
keywords: [testnet, 价格偏离, 策略盈亏, 失真, gate, binance, okx, PERP_FUNDING, realized_pnl]
scope: [autotrade, PERP_FUNDING, strategy_stats, auto-disable, testnet]
summary: testnet 价格偏离实盘会导致策略 realized_pnl 完全失真（gate testnet 偏离 ~$1090 → 0.01 BTC 亏 $11），进而触发 auto-disable 误判；代码链路验证可用 testnet，策略盈亏验证必须用价格接近实盘的 testnet（binance/okx）或真盘
first_seen: 2026-08-27
last_verified: 2026-08-27
skills: [事实核验]
evidence_refs:
  - type: db
    desc: "#370 gate:perp 开仓 BUY @80751 vs binance:perp SELL @79640 → 偏离 $1090 → realized_pnl=-$11.27"
  - type: db
    desc: "#370 binance:perp 开仓 SELL @79640 平仓 BUY @79672 → 价差 $32（正常波动）"
  - type: db
    desc: "strategy_stats PERP_FUNDING auto-disabled: 13 trades winRate=8% avgPnl=-$13.03（历史 LEGGED 手动平仓亏损拖累）"
  - type: commit
    desc: "436e4a7 D-121 gate:perp 移除白名单（2026-08-27）"
related:
  - d121-cex-position-display
  - d120-perp-funding-exec-chain
---

## 症状
#370 PERP_FUNDING:BTCUSDT:binance:perp:gate:perp 平仓后 realized_pnl=-$11.27。
业主判断"gate testnet 意义不大"。

## 根因
gate testnet 价格偏离实盘 ~$1090（BTC 开仓 BUY @80751 vs 实盘 ~79665）。
0.01 BTC × $1090 = $10.9 亏损 + 手续费 = $11.27。这不是策略亏损，是 testnet
价格失真。binance testnet 同笔 SELL @79640 → BUY @79672，价差 $32（正常）。

auto-disable 误判：历史 #360-#363 是 D-120 修复前的 LEGGED 仓位手动平仓
（-$18~-20），拖累 PERP_FUNDING winRate=8% / avgPnl=-$13.03 → 自动禁用。
实际策略本身 #367 +$0.35（正常 funding 差异收益）。

## 教训
- testnet 价值分层：**代码链路验证**（下单/平仓/reduceOnly/持仓查询）可用任何 testnet；
  **策略盈亏验证**必须用价格接近实盘的 testnet（binance/okx）或真盘
- testnet 价格偏离大的 venue（gate ~$1090）会污染 realized_pnl → 触发 auto-disable 误判
- auto-disable 的 winRate/avgPnl 包含历史 LEGGED 手动平仓亏损——非策略本身问题，
  需要区分"策略亏损"和"操作亏损"（LEGGED 手动平仓）
- 业主问"testnet 意义不大"是高价值信号——区分代码验证和策略验证两个层面

## 修复
D-121：gate:perp 从 ARB_AUTOTRADE_ALLOW_VENUES 移除，只用 binance+okx 验证策略盈亏。
全链路代码验证已通过 D-121 ①-④。gate 等真盘再启用。
