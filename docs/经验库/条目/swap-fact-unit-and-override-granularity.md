---
schema_version: 2
id: swap-fact-unit-and-override-granularity
kind: pitfall
risk: high
status: verified
keywords: [swap 单位, bps/day, pct_annualized, 误读, 免息 override, 前缀级, symbol 级, D-300, D-342, 口径, 账单对账]
scope: [swap/funding 事实采集与消费, 交易所字段口径假设, 免息/豁免白名单设计]
summary: 两个同族坑：①swap fact 单位是 bps/day（非 pct_annualized）——误读成年化再按短持仓摊会得出「8 倍口径差」假警报+误诊「断流」（实际 facts 连续在写，GROUP BY 显示假象）；以 venue 实收对账（±2% 内吻合=facts 正确）是唯一裁决法。②免息 override 用 server 前缀级判定=把实收 swap 的 symbol 变体也免了（Exness Trial5 标准形 XAUUSD 免但 247m/m 实收）——豁免白名单必须 (server, symbol) 粒度，"*" 通配只给账单级实证过的账户；账单证据优先于分类假设（D-112 旧分类被 492 实收推翻）。
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: []
evidence_refs:
  - type: doc
    ref: docs/D-342R1裁定与返修提示词-swap免息override收窄-2026-09-19-Claude.md（施工三证伪+终席裁定 A）
  - type: sql
    ref: pos 492 venue 实收 −0.55/日 vs fact −1.232bps/day 预测 −0.54（差 1.4%）
---
