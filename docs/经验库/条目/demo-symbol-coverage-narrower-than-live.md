---
schema_version: 2
id: demo-symbol-coverage-narrower-than-live
kind: pitfall
risk: medium
status: verified
keywords: [demo, trial, 品种面, 能源, PERP, broker, 开户, 组合空间, 竞品对比]
scope: [broker, 策略评估]
summary: broker demo 账户品种面普遍窄于实盘（Pepperstone/Hantec/CPT/IC demo 均无能源，PERP 永续为实盘专属）——组合空间评估和开户决策必须先确认 demo 品种面；竞品展示的实盘品种组合不能类推到 demo
first_seen: 2026-08-29
last_verified: 2026-08-29
skills: [KB维护]
evidence_refs:
  - type: server_fact
    ref: broker_symbol_map 实测：61585565=52 品种无油、510087714=49 无油、5208379=39 无油、52993526=54 无油；仅 390(CMC)/591(FxPro)/277(Exness) 有能源 swap
  - type: decision
    ref: decisions.md D-143（竞品油组合=CFD×PERP 跨形态，PERP 实盘专属）
repro_or_symptom: 照竞品平台清单开 demo 后发现高息差品种（能源）组合不出现——品种在 demo 品种面里不存在
root_cause: broker demo 默认品种集不含能源/exotic；竞品用户跑实盘（品种全）；demo 与实盘品种面差异被忽视
fix_and_prevention: 开户前问客服「demo 能否开通能源品种」；组合空间计算用实际 broker_symbol_map 品种面而非 broker 官网宣传清单；跨形态（PERP）策略默认实盘路径
notes: 一天连开三家 demo 均无油的实证（2026-08-29）；省开户时间的判别问句=「demo 自带能源吗」
---
