---
schema_version: 2
id: session-gate-mandatory-for-periodic-products
kind: pitfall
risk: high
status: verified
keywords: [marketstate, 会话闸, 伪警, 自盯]
scope: [alerting, marketstate, session]
summary: 会话制品种自盯/告警必过 marketstate 会话闸——漏接=闭市窗伪警洪泛（同族第三处）
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## 自盯/告警件对会话制品种必过 marketstate 会话闸

- **实证**：同族第三处——锚死告警→覆盖账本→前提塌检测，每处漏接会话闸=闭市窗伪警洪泛。
- **规则**：凡对会话制品种（MT5/CFD）的周期性自盯/告警件，必先过 `marketstate` 会话闸——闭市豁免、表缺席不误赦（fail-closed 照报）。新增告警件验收含「闭市窗零伪警」断言。
