---
schema_version: 2
id: venue-facts-have-shelf-life
kind: pattern
risk: high
status: verified
keywords: [venue, 合约清单, 实证保质期, BitMEX, gate, okx, 铂钯, XPT, 评估前提, 复拉]
scope: [research, venue-adapter, 评估票, 配对池]
summary: venue 合约面「已实证事实」有保质期——D-461 评估实测发现派单三断言全死（BitMEX 衍生品清零/gate 已上商品 perp/okx binance 已有铂钯），全死于 30~90 天内的 venue 侧上新或清退；评估/设计票引用 venue 面结论超 30 天必须复拉现网 API
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [事实核验, 启动与检索]
evidence_refs:
  - type: server_fact
    desc: "BitMEX /api/v1/instrument 全量 3109 个零 Open：XPTUSDT settled 2026-07-30、XAGUSDT 08-11、XAUTUSDT 09-02、XBTUSD 09-16；/instrument/active 仅剩 12 现货对（2026-09-24T15:58Z 拉取）"
  - type: server_fact
    desc: "gate /futures/usdt/contracts 直连：XPT_USDT/XPD_USDT/XAG_USDT/XAU_USDT/XAUT_USDT 全 in_delisting=false，XPT funding 实时流动（2026-09-24T16:0xZ）"
  - type: server_fact
    desc: "okx /api/v5/public/instruments 直连：XPT-USDT-SWAP/XPD-USDT-SWAP live，listTime=2026-02-02（2026-09-24T16:1xZ）"
  - type: doc
    desc: "报告=docs/研究/第4加CEX接入评估-2026-09-25-Claude.md §1 前提复核表"
related:
  - testnet-price-divergence-invalidates-pnl
  - gate-api-shape-must-match-sdk
---

## 症状
D-461 评估票（BitMEX/Bybit 第4所接入）开工复拉现网，发现派单背景三断言全部过时：
「BitMEX 有 XPT perp 已实证」→ 已结算清退；「gate 无商品合约结构性死」→ gate 已上
五个金属 perp；「okx/binance 无铂钯 perp」→ okx 2026-02-02 已上 XPT/XPD SWAP。

## 根因
venue 侧合约面是活物：新上市（okx 金属 2026-01/02、bybit 金银 2026-03-08）与清退
（BitMEX 2026 Q3 衍生品清零）都在 30~90 天尺度发生；文档里的「已实证」只是写下那一刻
的快照。经验库 staleness 报告只管 server_fact 条目，派单/设计文档内的 venue 断言无机制。

## 教训
- 评估/设计票引用 venue 合约面结论：超 30 天一律先复拉现网 API 再行文，复拉成本极低
  （公开 instrument 端点一次 curl），写错前提的代价是整张票方向错。
- 「X 所有/没有某合约」类断言在文档中必须带实证日期，读方按日期打折信任。
- 配对池扩容类需求先查 `fin_cex_universe`（venue API 实采，last_seen 新鲜）再谈接新所。

## 规则
1. venue 面「已实证事实」引用超 30 天 → 必须复拉，禁直接引用。
2. 写 venue 断言必须带实证日期戳。
3. 配对池/venue 扩容议题第一步 = 查 fin_cex_universe 场内覆盖现状。
