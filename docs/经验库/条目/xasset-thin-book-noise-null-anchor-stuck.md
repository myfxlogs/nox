---
schema_version: 2
id: xasset-thin-book-noise-null-anchor-stuck
kind: pitfall
risk: medium
status: verified
keywords: [xasset, noise_bps, 簿饥饿, 锚死告警]
scope: [xasset, monitor, anchor, diagnosis]
summary: 锚死告警未必伪警——簿饥饿(<12行/日)→noise NULL→补锚死循环；诊断先核对级日产行数
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## xasset 簿对饥饿→noise NULL→补锚径结构性不能收口

- **实证**：BZUSDT-BRENT×3001157 簿 10-10~10-18 仅 ~7 行/日（10-09 还有 169 行）→trailing24h <12 样本→noise_bps 恒 NULL→LatestXAssetEntryGate ok=false→entry_basis 补不上→锚死告警 9 天。
- **教训**：「簿在但锚死」告警未必是伪警——会话闸只消闭市伪警分量；noise 饥饿（簿行稀疏/断供）是真病灶层。诊断锚死先核「对级日产行数≥12 否」。
- **收口实证**：开市簿复产（5min 节奏）满 12 样本后 accrue 即补锚成（67254@-68.1bps）。
