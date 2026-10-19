---
schema_version: 2
id: broker-symbol-map-dual-column-key-domain
kind: pitfall
risk: medium
status: verified
keywords: [broker_symbol_map, canonical, 键域]
scope: [pgstore, symbol-map, key-domain]
summary: broker_symbol_map 双列分工：读表/诊断用 canonical、下单用 broker_symbol（同族第 6 例）
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## broker_symbol_map 双列键域分工：读表 canonical、下单 broker_symbol

- **实证**：backtest 诊断键初版取错列（c0a3ff7d4），SG 实测 BTCUSDTm 零 tick 实锤——canonical=ticks/存面键，broker_symbol=MT 线协议键（BTCUSDTm）。
- **口诀**：读表/诊断/查询 ticks 取 canonical（BTCUSDT 族）；下单/venue 协议面取 broker_symbol。同族病灶第 6 例。
