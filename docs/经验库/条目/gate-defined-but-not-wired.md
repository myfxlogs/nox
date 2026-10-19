---
schema_version: 2
id: gate-defined-but-not-wired
kind: pitfall
risk: high
status: verified
keywords: [死闸, 接线, 调用点, 机制件]
scope: [review, integration, gate]
summary: 机制件定义/flag/单测齐但主径零调用=死闸——「已并入」≠「已生效」；验收含主径调用点断言
first_seen: 2026-10-18
last_seen: 2026-10-18
---

## 合入件「闸未接线」：定义在/flag 在/测试在但主循环零调用

- **实证**：bnprobe weightGate @24a3ab8ca 并入件——定义、配置 flag、单测全齐，主循环零调用=死闸；o0 残段补跑票开工审计抓获，接线后首跑即截批实证闸活（@87717656b）。
- **教训**：依赖某机制作前置闸的开工预检，必须先 grep 调用点实证接线（定义存在≠生效）；机制件验收应含「主径调用点存在」断言，非仅函数级单测。
- **等级**：high（风控类闸死接=假防护）。
