---
schema_version: 2
id: carry-rate-is-not-expectancy
kind: pitfall
risk: high
status: verified
keywords: [carry, 年化收益率, 期望值, 路径风险, 止损带宽, 基差噪声, tracking error, 被扫概率, 评估口径, 理论vs现实]
scope: [carry/息差类策略的评估与开仓决策, 任何带止损的价差策略, 机会卡/评估展示层设计]
summary: carry 类评估卡展示的「年化收益率」是持有且基差中性假设下的速率，不含路径风险；当止损带宽 < 基差噪声 σ 时交易期望值为负（被扫是大概率事件），理论速率再高也是学费机——速率与期望值的换算检验：carry 收益是 bps/天 的小数级慢变量，基差噪声是 bps/小时 的个位级快变量，任何「带宽<σ」配置下应先算期望值 P(不被扫)×carry 累积 − P(被扫)×带宽成本，再谈速率展示。修法=决策门用期望值/安全边际口径（顺价深度−TE），评估层速率与安全边际并展；为验证链路而强开的仓不适用盈利预期评价。
first_seen: 2026-09-18
last_verified: 2026-09-22
skills: []
evidence_refs:
  - type: doc
    ref: docs/handoff/LOG.md「posID=488 每日总结①」「D-332 复审通过收编部署」段（488 expected_net_bps=8.10 年化 vs realized −$2.46/6.1h 解剖）
  - type: sql
    ref: 485 −$1.25/488 −$2.46 两例+D-256 25 单 −$44 胜率 4% 同墙（实测摆幅：488 entry −14.85→−19.7bps/6h）
  - type: doc
    ref: 勘误注记（9-18 晚）：fin_xasset_monitor.tracking_error_bps=当期基差水平非滚动 σ（工具源码 |tokenMid−xauMid|，与 |entry_spread| 恒近等）——引用作噪声=误读，噪声判定须用摆幅类指标（D-335 v2 noise_bps）
  - type: doc
    ref: docs/D-378b设计与施工提示词-XASSET窄阈止损改灾难带-2026-09-22-Devin.md §1.1（493-496 四连杀全误杀：实亏 −$18.57 vs carry +$1.26，死后基差 5-12min 全回复；495 噪声极值偏离 −15.2bps≈2.2σ；判距=max(4,noise≈6.7-7.5)≈1σ 贴线止损定理实证）→ 处置落地=窄阈 level-stop 撤除改灾难带（max(40,4×noise)+新鲜度闸+去抖），delta 中性 carry 仓的基差反向移动是浮亏非实亏
---
