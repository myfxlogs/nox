---
schema_version: 2
id: pulsed-signal-continuity-gates
kind: pitfall
risk: high
status: verified
keywords: [脉动信号, 结构性零值, 连续性门槛, worst 判据, 零期, FN 率, funding 零期, 门槛校准, 假阴性, 连续 N 期]
scope: [对含结构性零值的信号设计触发/准入门槛, funding/regime 类判定, 任何「连续 N 期 X」或「窗口内任一样本破线」语义的守卫设计]
summary: 对结构性含零的脉动信号（如 funding 零期占 45%）设「连续 N 期正」「窗口内任一样本破 floor」类门槛，零值会断连/触底——对真 positive regime 产生大假阴性（实测字面口径仅放行 43% 时间窗，57% 时间误卡一个 0 负期的全程正 regime）甚至永久拒绝。病根=连续性/最坏值语义默认信号连续。修法=非零期口径（零≠风险，负才是——负值出现照样断连拦截，防线不降）；同族修法=worst 判据改 mean+latest 双锚。设计门槛前必先测信号的零值占比（一条 SQL 分桶），入口与退出侧口径须显式对齐。
first_seen: 2026-09-18
last_verified: 2026-09-18
skills: []
evidence_refs:
  - type: doc
    ref: docs/handoff/decisions.md「D-324 修订①」（30d 实测 180 期/0 负/45% 零：字面 43% vs 零期不计 100% 放行）
  - type: doc
    ref: docs/D-334三新发现解决方案-2026-09-18-Claude.md §2（D-328 worst=0 永拦同病根）
---
