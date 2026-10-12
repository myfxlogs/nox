---
schema_version: 2
id: business-assumptions-in-code-audit
kind: pitfall
risk: high
status: verified
keywords: [业务假设, 硬编码, 周三三倍, funding 频率, 结算日历, 符号级字段, 双口径, 静态审计, 实证]
scope: [exec, collect, strategy, workflow]
summary: 代码里的业务真相假设（结算日/频率/字段粒度）不会被运行时验证自动纠正——需静态审计分类；实证：周三三倍实为周日、funding 年化双乘 3 倍、符号级字段被当 venue 级
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: 修复⑪A 施工提示词（全仓假设审计 P0）
  - type: doc
    ref: docs/handoff/LOG.md 2026-08-21 三倍日实测
---

## 事实/症状

业主铁律「所有真相必须服务器验证」在全仓审计中抓出 14 处违规：①「周三三倍 swap」从项目初期写进 STATE 的假设被实测推翻（实际周日）；②funding 记账层复乘采集层已做的年化换算（×3 虚记）；③`ThreeDaysSwap` 是**符号级**字段被当 venue 级取值且 map 遍历序随机（每轮可能不同值——连实测结论都不稳）；④平仓 PnL 简化公式漏 ContractSize（XAUUSD 差 100 倍）直灌 realized_pnl 污染熔断与淘汰决策。

## 根因或策略解释

业务假设写进代码后**没有任何机制自动验证它**——运行时一切正常（不报错、交易照做），错误只在结算日/平仓那天显形。且「字段粒度假设」（符号级 vs 账户级 vs venue 级）是隐藏最深的子类：字段存在、值合理、粒度错了。

## 处置

1. 分类审计标准（违规A：业务真相写死应读服务器 / 违规B：未标注假设 / 合规：env 默认值+fail-closed 降级+工程常量）——探员任务书模板见本条目 evidence。
2. 新增服务器数据接入时必答三问：字段在哪一级（符号/账户/venue）？跨级一致有实证吗？map 遍历取"首个"是否引入随机性？
3. 金额路径（PnL/记账/熔断基准）一律服务器权威值（OrderClose Profit），公式仅展示层预估；双层换算（采集层已年化）要查上游口径。
4. 被推翻的假设值留在代码当 fallback = 已知错误兜底，必须 fail-closed。

## 证据

- check: 修复⑪A 五项修法各带回归锚（XAUUSD 100 倍/f=0.0001 断言/per-symbol 全景表）
- result: 审计 18 项分级（P0 五项修复⑪A / P1 八项 / P2 存疑三项）
- ref: 全仓假设审计探员报告 2026-08-21

## 反证条件

- check: 后续新增业务假设是否全部带实证标注或 env 化
- falsifies_when: 接入层统一 per-symbol 字段模型后 → 粒度子类失效，条目收窄为金额路径纪律

## 适用边界

所有读外部系统数据（broker/交易所）的代码。纯内部工程常量不适用。
