---
schema_version: 2
id: multi-step-fund-half-failure-residue
kind: pitfall
risk: medium
status: verified
keywords: [JitSweep, 半程残留, 多步资金, 反向补偿]
scope: [dciexec, fund-transfer, residue]
summary: 多步资金操作败在 N>1 步须有反向补偿或残留告警——禁假设下游兜回（JitSweep $1000 滞留 11min 实证）
first_seen: 2026-10-18
last_seen: 2026-10-18
---

## 多写径异步败=半程资金残留（JitSweep 赎回成+二跳 403）

- **实证**：JitSweep 赎回（io 落账）成功+二跳划转 403→abort 无回补，$1000 滞留 spot 11min，靠 autoexec 顺手救回（非设计径）。
- **教训**：多步资金操作每步独立提交时，败在第 N>1 步必须有「已做部分反向补偿」或「显式残留告警」——禁假设下游件兜回。
