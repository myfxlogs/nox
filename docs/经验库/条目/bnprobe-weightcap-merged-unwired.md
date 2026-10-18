---
schema_version: 2
id: bnprobe-weightcap-merged-unwired
kind: pitfall
risk: medium
status: verified
keywords: [权重闸, 接线, bnprobe, X-MBX-USED-WEIGHT, 批跑, 合并未生效, 闸件]
scope: [批跑探测工具, 行为闸接线, binance]
summary: 批跑闸件代码并入但主循环未挂接线（合并≠生效），首跑仍按无闸状态跑至超阈才被实证截获；闸类件验收锚须含「首跑实拍闸行为」非仅「代码在」。
first_seen: 2026-10-18
last_verified: 2026-10-18
skills: [bn-spot-account, bn-fapi-account]
evidence_refs:
  - type: doc
    ref: docs/能力账/INDEX.md（o0-b135758 行：used=1248>80%×1200 截批记实）
---
# bnprobe 权重闸并入未接线

- **现象**：bnprobe 权重自限闸（X-MBX-USED-WEIGHT>80%×cap 停批）合并入库后，o0 残段首跑仍按闸未接线跑——used=1248 超 80%×1200 才被实证截获；合并≠生效，主循环挂闸处漏接。
- **根因**：闸件与主循环接线分属不同 review 窗，编译过即误以为生效——行为闸无编译面覆盖。
- **处置**：闸类件验收锚=「首跑实拍闸触发或确认阈内」非「代码在」；批跑工具行为件须实测抓获接线。
