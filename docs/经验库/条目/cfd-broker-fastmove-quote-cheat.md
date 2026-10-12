---
schema_version: 2
id: cfd-broker-fastmove-quote-cheat
kind: pitfall
risk: high
status: verified
keywords: [CFD, MT5, Exness, broker 作弊, 报价冻结, 报价滞后, 快行情, 灾难带误杀, disaster band, XASSET_CARRY, basis]
scope: [autotrade, xasset, mt5, disaster, quote]
summary: CFD broker（Exness）大行情冻结/滞后报价 30min~1h 是常态作弊——「时间戳新鲜但价格滞后」的报价能穿透报价龄闸，凡引用 CFD 腿价的 kill 判定都会被假价穿透；carry 仓 kill 判据只能是 CEX 腿 vs crypto 共识的结构断裂
first_seen: 2026-10-07
last_verified: 2026-10-07
skills: [生产取证]
evidence_refs:
  - type: decision
    ref: D-111
  - type: commit
    ref: 1cfec01e4
  - type: decision
    ref: D-113
  - type: server_fact
    source: postgres
    query: "positions id=509 → close reason `disaster: dev 42.0 > band 40.0`（12:28:35Z）；成交时 basis≈-13bps 即 dev≈3.5——判定用的是一个 2 秒窗口内的滞后假价"
    observed_at: 2026-10-07T12:28:35Z
---

## 事实/症状

#509（XASSET_CARRY XAUUSD，3001157 MT5×okx perp）在 XAU 急跌中被灾难带平仓：
判定时刻跨所 basis 瞬时撕开 -51.6bps（锚 -9.64，dev 42>40）；2 秒后实际成交时
两腿已追平（MT 4084.068 / OKX 4089.5，basis≈-13bps）。realized -$4.17。
业主裁定该行为为 broker 常态作弊而非 venue 断裂。

## 根因或策略解释

灾难带的新鲜度闸（XAssetQuoteMaxAge=5min）只挡「时间戳旧」的报价，挡不住
「时间戳新但价格滞后」的报价——broker 在大行情中持续打新鲜时间戳的旧价/跳价。
此时跨所 basis=buyBid(MT)−sellAsk(CEX) 的分母一条腿是假的，算出的偏离不是
venue 断裂信号。同理 CF D 腿冻结期间报价干脆缺席（30min~1h），该窗内跨所价差
根本算不出来。debounce=2 在 ~1s/轮的扫描节拍上只挡得住 <2s 的尖刺，挡不住
「滞后数秒」这种作弊窗。

## 处置

- **通用律（D-113 已落地）**：XASSET kill 判定**永不引用 CFD 腿价**——MT 偏离
  降级为告警（人工裁定域），唯一 kill=CEX 腿 mid vs crypto 共识中位的锚后
  偏移。快行情闸（D-111）只盖住位移窗，窗后阴跌+持续滞后仍可穿透——所以
  正解不是「加更多闸保护 MT 价」，而是「kill 算式里根本没有 MT 价」。
- **不对称信任**：CEX 腿价可信（多源可校验，共识≥2 源中位防独苗歪价），
  CFD 腿价只做持仓估值参考+有无判定（stale→挂起）。
- **共识判「锚后偏移」非绝对差**：金代理源（XAUT/PAXG/perp）间常驻价差
  实测 10-20bps（代币发行溢价），首观测锚定吸收之，判的是锚后结构断裂。
- **冻结 vs 滞后是两形态**：冻结=无报价（报价龄闸兜住=挂起不平），滞后=
  有报价但价假（位移闸+kill 不引用双保险兜住）。

## 证据

- check: `SELECT reason FROM autotrade_logs WHERE position_id=509 AND status='CLOSE_TRIGGER'`
- result: `exit: disaster: dev 42.0 > band 40.0`；position_legs 两腿 FILLED
  成交价差 -13bps（dev≈3.5 远低于带）= 误杀实证
- ref: D-378b 灾难带原设计依据 495 实测噪声 15.2bps——该样本未覆盖快行情
  broker 滞后形态，新样本（本次）已超其极值解释域

## 反证条件

- check: 观察后续快行情中 fastMarketSuspend 是否反而漏放真断裂（CEX 腿自身
  mark/index 脱锚）
- falsifies_when: CEX 侧出现 venue 级断裂而 CFD 腿仍在动——位移闸挂起窗口内
  真断裂会延迟到行情回稳才判（D-113 已把 kill 换成 CEX 共识判定，此风险只剩
  「挂起窗内 CEX 断裂延迟平仓」一形态）
- falsifies_when: broker 价**永久性**脱离全球金价而 CEX 侧无恙——D-113 不自动
  平只告警，须人工裁定（设计取舍，非缺陷：Stage 1 名义敞口小）

## 适用边界

XASSET_CARRY 及一切含 CFD 腿的 kill 判定。SWAPARB/CARRY 纯 CEX 对不受影响
（双腿同为可信 venue）。位移阈 25bps/90s 是首版标定——过快行情误挂/静市
误判都可通过 ARB_XASSET_FASTMOVE_* 调，不必改代码。
