---
schema_version: 2
id: removal-closure-grep-vs-kept-names
kind: pitfall
risk: low
status: verified
keywords: [闭包断言, 删除闭包, grep模式, 前缀匹配, 保留清单, 文件改名, 假阳性, 删除型工单]
scope: [web前端, 删除型工单, 验收grep设计]
summary: 删除型工单的 grep 闭包断言用宽前缀模式（如 OpportunityPage）时，与显式保留清单中的文件名（OpportunityPageVenueTag.tsx）前缀相撞——保留件命中断言=假阳性，执行者要么误删保留件、要么给断言开豁免削弱闭包、要么漏执行；修法=更名保留文件收窄前缀或模式精确到 token 边界；断言集定稿前必须与保留清单交叉核对。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-402
  - type: commit
    ref: dcc8c808
  - type: doc
    ref: docs/D-402设计与施工提示词-机会分析与价差矩阵两tab摘除-2026-09-23-Devin.md:26
---

## 事实/症状

D-402 同票两断言自相矛盾：§1 明令「`OpportunityPageVenueTag.tsx` 保留（OverviewPage 引用 VenueTag）」，§4 闭包断言「`OpportunityPage` 在 web/src 零命中」——保留件文件名含该前缀，必然命中。若不处理：断言永久红（验收不可达）、或豁免保留件（闭包开洞）、或顺手误删保留件（能力丢失）。

## 根因或策略解释

闭包断言模式按「记忆中的族名」写（OpportunityPage 族），未与保留清单做程序化交叉核对——族边界在命名上不闭合（保留件共享族前缀）。宽前缀断言的隐含假设「命中即可删」与保留清单冲突。

## 处置

三选一，按破坏面最小裁：①更名保留文件收窄前缀（D-402 实操：OpportunityPageVenueTag.tsx→VenueTag.tsx，git rename 100%，唯一引用点同步）；②断言模式精确化（如 `\bOpportunityPage\.(tsx|ts)\b`）；③断言行内显式豁免清单（最后手段——豁免即闭包开洞，须在设计文档留痕）。定稿前程序化交叉核对：对每个断言模式跑一次全仓，命中集 ⊆ 删除集。

## 证据

- check: 断言模式全仓 grep 命中集 vs 工单保留清单 diff
- result: OpportunityPage 模式命中保留件（矛盾实证）；更名后闭包全零（dcc8c808）
- ref: D-402 §1 vs §4；dcc8c808；R1 复审清单 §6（5c73a183）主体通过背书

## 反证条件

- check: 断言集×保留清单交叉核对程序化执行
- falsifies_when: 交叉核对通过仍出现保留件被断言命中 → 保留清单本身不完备（工单层缺陷，非断言层），本条目结论需扩写

## 适用边界

一切以 grep/正则做「删除闭包零残留」验收的工单；模式为子串/前缀语义时必检。逐 token 边界的精确正则风险天然较低。
