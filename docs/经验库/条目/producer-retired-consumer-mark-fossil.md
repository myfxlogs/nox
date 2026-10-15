---
schema_version: 2
id: producer-retired-consumer-mark-fossil
kind: pitfall
risk: medium
status: verified
keywords: [产者撤除, 消费者残值, 数据化石, 数据龄, spot_depth, sysretro, 告警维度, D-033]
scope: [facts, consumers, reports, sysretro, 数据面]
summary: 产者撤除后消费者仍静默读残值=隐形失真——sysretro 深度段吃 8 天化石零告警；规则：撤数据源须同帧评估消费面，残值显标数据龄/化石不冒充现值（D-531）
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [生产取证, code-audit]
evidence_refs:
  - type: code
    ref: ab59fecde（10-07 撤 cex_depth_sampler）→ sysretro depth_section.go 读 8 天残值
  - type: decision
    ref: decisions.md D-531（数据龄为自盯新维；数据源撤除须同帧评估消费面）
  - type: decision
    ref: decisions.md D-532 次缺陷——data_lost 58 行退出零告警同型
repro_or_symptom: 报表/消费者段持续出「正常」数据但实为撤除产者的终值快照；告警面无「数据龄」维度故零提示——用户以为现值实为化石
root_cause: 撤产者的验收只看产者不再跑，未核下游消费面；告警判据有「新鲜度阈值」（表级）无「数据龄」维（行级残值不触发）
fix_and_prevention: ①撤数据源/产者的票必须同帧列消费面评估（谁在读、读到的会是什么）②消费者读旧值须显式标注（化石注+最新样本日期）③告警维补数据龄类（paperExitNote 无挂点已立喂流自盯票）
notes: 同族：stale-data-propagates-wrong-judgments（陈数误判断）；差异=本坑专指「产者撤除」这一成因的化石化
---

## 现象

`cex_depth_sampler` 按计划撤除（ab59fecde）后，sysretro 周报复盘深度段继续消费 8 天前的 spot_depth 残值行，无化石标记无告警——看似现值实为死数据。

## 根因或策略解释

撤产者验收只查产者停跑，消费面未同帧评估；告警体系只有表级新鲜度阈值，行级残值无「数据龄」判维。

## 处置

消费者加化石标（latest 样本龄>48h 章首注数据化石+截止日，@9a647db60）；新增规则：数据源撤除须同帧评估消费面（D-531）。

## 证据

- check: SG facts spot_depth_* 最后写 10-07 vs sysretro 仍出表
- result: 化石标已挂，产者不复建
- ref: 9a647db60 / D-531

## 反证条件

- check: 若消费面自停（产者停即不读），无需化石标
- falsifies_when: 消费面已自带龄显——则本坑仅适用无龄显消费面

## 适用边界

一切「产者撤除但历史行保留」的消费面（facts/报告/复盘段）。
