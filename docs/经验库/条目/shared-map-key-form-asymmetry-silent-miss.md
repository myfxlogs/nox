---
schema_version: 2
id: shared-map-key-form-asymmetry-silent-miss
kind: pitfall
risk: high
status: verified
keywords: [key形不一致, typed键vs裸键, BareOf归一, equiv解析静默失能, monitor簿, 误报告警, NULL账]
scope: [asset_class_view, cfdPairCandidates, monitorVenueKeys, keydomain, 跨域键查找]
summary: 共享 map 写侧归一（BareOf 裸键）读侧直查（typed 键）→ 查找恒 miss 且无任何报错——等价类解析静默失能，表现为下游误报「簿缺」与账列恒 NULL。
first_seen: 2026-10-09
last_verified: 2026-10-09
skills: [事实核验, 代码审计]
evidence_refs:
  - type: commit
    ref: 7a102d9c8
  - type: doc
    ref: internal/scan/asset_class_view.go
  - type: doc
    ref: internal/scan/paper_cfd_book_gate.go
  - type: server_fact
    source: postgres
    query: SELECT pair,cex_venue,mt_venue FROM fin_xasset_monitor WHERE pair LIKE '%SILVER%' -- 簿行实存 vs warn 报缺并存
    observed_at: 2026-10-09T07:41:12Z
---

## 事实/症状

D-144 部署后生产告警 `active 仓 monitor 簿缺席 … 簿:SILVERUSDT-SILVER|okx:perp|3001157:mt5`，
而 `fin_xasset_monitor` 里 `XAGUSDT-SILVER|okx:perp|3001157:mt5` 行正在正常写入——**簿在、报缺**。
更深后果：`insertCfdPaper`/`accrue` 的 entry basis 查簿同径 miss，等价类品种（SILVER→XAGUSDT 等）
新仓 `entry_basis_bps`/`est_round_trip_fee_usd` 恒 NULL，毕业链对这些仓永久断流。

## 根因或策略解释

`assetClassView` 的四个 venue 键 map（`cexClass/mtClass/cexByCls/mtByCls`）按 `asset_equiv.venue`
**裸名**建键（`SILVER|3001157`）；而读侧四处（insert 开仓、accrue 闸、两条簿缺哨兵）经
`monitorVenueKeys` 产出 **typed 键**（`3001157:mt5`/`okx:perp`）直接查找 → `map[class|3001157:mt5]`
恒 miss → `cfdPairCandidates` 只剩 legacy 候选 `SILVERUSDT-SILVER`。写侧（xasset-monitor
`pairs.go`、`bookUncoverableReason`）恰好都先 `BareOf` 故正常——**同一张 map，读写两键形，
miss 不报错**，纯静默。

教训两点：

1. **键形归一必须放查找原语内**，不能依赖调用方自觉——一处调用方换键形，整张查表静默失能。
   `BareOf` 对裸入参幂等，归一零代价。
2. **「候选列表兜底」掩盖故障**：legacy 键先行+first-hit 的设计让 miss 退化为「恰好只剩
   legacy 候选」，告警文案只显示最后试的键，把解析失败伪装成「簿真缺」。审计时看清候选数——
   单候选即解析未生效的信号。

## 修法记录

`mtCanon`/`cexCanons`/`mtClassOf`/`cexClassOf` 内统一 `keydomain.BareOf(venue)`；
锚测 `TestD146_TypedVenueKeyResolvesEquiv`（typed×裸四组合同解析）。
