---
schema_version: 2
id: setter-injection-wiring-gap
kind: pitfall
risk: high
status: verified
keywords: [装配断链, setter 注入, 构造注入, 零调用方, 装配锚, 测试直连, 编译期守卫, 忘接线, A8 未验陈述]
scope: [依赖注入方式选择, 装配/wiring 类缺陷的测试锚设计, 复审验收装配链]
summary: setter 注入（SetXxx 无调用方约束）天然可忘——零件（缓存/依赖/计算）全对、测试全绿（测试直连构造），生产装配链断开零症状（本例：tzCache 恒 nil 页面全空态，业务无报错）。防法三件：①关键依赖用构造必选参数注入（忘接=编译失败，编译期守卫强于测试守卫）；②装配类验收必须 grep 注入 setter 的调用方（零 caller=断链）且变异测试要红在「摘生产调用点」而非「摘函数体」——直连调用函数的测试永远守不住调用点遗忘；③复审 A8 只写已验事实（「按需拉取」这类未验陈述会误导后续排障）。
first_seen: 2026-09-18
last_verified: 2026-09-19
skills: []
evidence_refs:
  - type: doc
    ref: docs/handoff/LOG.md「D-337R1 复审：S2/S3 过、S1 退回 R2」段（M1 变异摘调用仍绿实证）
  - type: doc
    ref: docs/D-337R2返修提示词-tzCache构造注入-2026-09-19-Claude.md
---
