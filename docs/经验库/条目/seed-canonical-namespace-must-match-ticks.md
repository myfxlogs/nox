---
schema_version: 2
id: seed-canonical-namespace-must-match-ticks
kind: pitfall
risk: high
status: verified
keywords: [asset_equiv, canonical, 迁移种子, ticks.symbol, venue 原生名, 配对不成对, XAssetCarry]
scope: [asset_equiv/broker_symbol_map 种子迁移, detector 配对, CEX perp 多 venue]
summary: 迁移 0100 用 venue 原生合约名（CL_USDT/CL-USDT-SWAP/NAS100_USDT）写 asset_equiv canonical，但 ticks/facts canonical 全所同键 <base>USDT——跨所腿与 SILVER mt5 腿永不命中配对，部署后运行取证才暴露。
first_seen: 2026-09-16
last_verified: 2026-09-16
skills: [事实核验, 生产取证]
evidence_refs:
  - type: commit
    ref: 7968fd9c # D-308 勘误 0101
  - type: server_fact
    source: sql
    query: SELECT symbol,venue FROM ticks WHERE venue='gate' AND symbol LIKE 'CL%' # → CLUSDT 非 CL_USDT
    observed_at: 2026-09-16T08:04:00Z
---

## 事实/症状

D-308 迁移 0100 上线后：asset_equiv 跨所 perp 腿写 `CL_USDT@gate`、`CL-USDT-SWAP@okx`、`NAS100_USDT@gate` 等 venue 原生合约名，detector 零候选且无任何报错——静默不成对。

## 根因或策略解释

ticks/facts 的 canonical 命名空间是**采集器派生的全所同键** `<base>USDT`：gate `CL_USDT`→存 `CLUSDT`，okx `CL-USDT-SWAP`→存 `CLUSDT`（见 cex-canonical-fallback-from-symbol）。asset_equiv canonical 必须与该命名空间一致才能命中 `snapshot[canonical][venue:vt]`。venue 原生名只在 venue API 层存在，永不进入 canonical 层。同理 MT5 腿 canonical 要查 broker_symbol_map 既有生态值（XAGUSDm→SILVER 已 10 venue 在档），不能新造 XAGUSD。

## 处置

- 种子迁移写 canonical 前，**必须拿 ticks.symbol 生产实拍对拍**（`SELECT symbol,venue FROM ticks WHERE symbol LIKE '<base>%'`），禁止照抄 venue 文档/雷达原生名。
- 同 canonical 多 venue 行是同组备选报价；同 canonical 跨所配对是 CrossVenueDetector 领地，XAssetCarry 只配不同 canonical。
- 修复形态=迁移 0101 DELETE+INSERT 幂等归一（UPDATE 撞 unique(asset_class,canonical,venue)）。

## 证据

- check: `SELECT symbol,venue FROM ticks WHERE venue IN ('okx','gate') AND symbol LIKE 'CL%'`
- result: 全存 `CLUSDT`；迁移后 GOLD 跨所候选实产 `XAUUSDT→PAXGUSDT @okx/gate`
- ref: 7968fd9c / docs/D-308复审-通过-2026-09-16-Devin.md / LOG 2026-09-16

## 反证条件

- check: 若采集器改为 venue 原生名存 ticks.symbol
- falsifies_when: asset_equiv 用原生名可命中配对——当前无此路径

## 适用边界

asset_equiv/broker_symbol_map 及一切按 canonical 查 ticks/facts 的种子数据；fin_cex_universe 存 venue 原生名是正确的（那是 universe 命名空间不是 canonical）。
