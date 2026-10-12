---
schema_version: 2
id: api-response-fields-empirical
kind: pitfall
risk: medium
status: verified
keywords: [API, 响应字段, positionRisk, initialMargin, okx sim, imr, 假值, 字段想当然, 实测, fixture]
scope: [reconcile, cex, adapter]
summary: 交易所 API 的响应字段与环境行为必须以实测响应为准（文档/直觉字段可能不存在或环境异变）——binance positionRisk 文档级 initialMargin 实测响应不存在（全仓无 per-position margin，须 notional/leverage 计算）；okx sim 环境 imr 为恒定假值（$1 名义也占 ~$333）；同族前例 gate-api-shape-must-match-sdk
first_seen: 2026-08-29
last_verified: 2026-08-29
skills: [KB维护, 生产取证]
evidence_refs:
  - type: code
    ref: internal/reconcile/cex_positions.go（D-146 S5 微件③ 返修①注释+sim 置 0）
  - type: server_fact
    ref: 带签名实测：positionRisk 200 返回含 notional/leverage/isolatedMargin 无 initialMargin；okx positions sim 返回 imr≈$333 恒定（$1 名义 TRX 也占 $332）
  - type: decision
    ref: decisions.md D-146 S5 微件③（字段错配根因记录）
repro_or_symptom: 按 API 文档假想字段（如 initialMargin）写解析→值恒 0；或 demo/sim 环境返回看似合理实则恒定的假值（保证金列全 ~$333）→ UI 误导（业主实证「okx 保证金都这么高？」）
root_cause: 第三方 API 的文档字段集≠实际响应字段集（版本/账户模式裁剪：binance 全仓 positionRisk 无 per-position margin）；模拟环境（okx sim）的保证金类字段可为无意义恒定值
fix_and_prevention: ①接新端点先带签名 curl 实测，把真实响应样本贴进单测 fixture（防再次字段想当然）②全仓模式保证金用 notional/leverage 计算③simulated 环境的存疑字段置 0 不展示（宁缺毋假），真金自然恢复④同族检查 gate-api-shape-must-match-sdk
notes: 检测信号：某字段全环境恒 0（想当然）或恒常数且与名义无关（环境假值）——两种都该触发实测复查
---
