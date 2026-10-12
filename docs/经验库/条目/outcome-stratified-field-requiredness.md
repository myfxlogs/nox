---
schema_version: 2
id: outcome-stratified-field-requiredness
kind: strategy
risk: medium
status: verified
keywords: [历史回溯, 样本库, 字段承重, 退市, 存活偏差, EDGAR, 13E-3, Yahoo, 建库]
scope: [D-274, event_cases, 案例库, 数据管道]
summary: 历史交易样本库的"必填字段"须按统计消费方分 outcome 评估再定获取策略，禁全字段等权——本案例 going-private 库 success=退市价不可得但非承重（drawdown 只算 failed），failed=活跃价可得；先画字段→统计依赖图可省两轮管道修补迭代
first_seen: 2026-09-15
last_verified: 2026-09-15
skills: [KB维护, 事实核验]
evidence_refs:
  - type: doc
    ref: docs/D-274讨论-事件驱动机会研判引擎-2026-09-14.md（终裁四/五/六段）
  - type: commit
    ref: cbf31f15
---

## 事实/症状

D-274 历史案例库（going-private 交易）建库：pre_price 富集三连败——①Yahoo 无退市数据（TWTR 实测）②EDGAR 元数据 tickers=[]（KnowBe4 实测）③公文明示值抽取不可补率 83.3%。每轮都在修"通道"，第三轮（终裁六）换分析法才解。

## 根因或策略解释

字段获取策略被当成全字段等权问题（每个样本都要补齐所有字段），实际字段承重性按统计消费方**分 outcome 分层**：AvgDrawdown 只算 failed 案例（M3 v4.6 M2 原定义）→ failed 样本（公司仍活跃）价格必得且必补；success 样本（已退市）价格不可得但 drawdown 统计本就排除它 → pre_price 非承重可缺（premium 置 NULL）。**承重路径天然可补，退市阻塞的只是非承重字段。**

## 处置

- 建库/回溯类任务开工前先画**字段→统计消费方依赖图**，按消费路径标注每字段的 outcome 分层承重性，再定获取策略与哨兵口径。
- 哨兵分母对齐承重口径（failed 样本承重字段不可补 >40% 停），勿用全字段覆盖率——全字段口径会把"设计内可缺"误报成管道病，诱发放宽阈值或换源的误处置。
- 工作序反转：outcome 回填先行，价格富集按 outcome 需求做（非全量先富集再回填）。

## 证据

- check: tickers.json 命中实测（2021-2023 13E-3 公司级 5 样本：退市 3=success 全缺席/活跃 2=failed 命中 SQNS/AMBR）
- result: failed=活跃=Yahoo 必得（承重路径 100% 可补）；success=退市=pre_price 非承重
- ref: docs/D-274讨论-…md 终裁六；M3 §3 v4.6 M2「actual_drawdown 只算 failed」

## 反证条件

- check: 若库统计改为全 outcome 算 drawdown（含 success 回撤），或 failed 样本活跃率显著下降（如老样本 failed 公司也批量退市）
- falsifies_when: failed 样本价格不可补率超哨兵 40% → 「承重路径天然可补」失效，须回全量通道方案

## 适用边界

- 适用：任何按 outcome 分层消费字段的回溯库/统计管道（交易案例库/复盘库/审计抽样）。
- 不适用：全 outcome 等权消费的字段（如去重键 code——须恒可解，本案用 CIK 双格式兜底）。
- 仅本项目 D-274 语境验证；泛化到其他域时先画依赖图复用方法而非结论。
