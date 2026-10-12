---
schema_version: 2
id: fix-must-enumerate-all-writer-paths
kind: pitfall
risk: high
status: verified
keywords: [字段级修复, 写入路径枚举, WS事件流, REST, 主导源盖回, buffer_multiple NULL, 部署取证, 生产面漏修, okx_event]
scope: [后端, 采集链, reconcile, dashboard, 持久化字段]
summary: 修复持久化字段的缺陷只改了低频写源（REST/scheduler），高频写源（WS事件流）同病未修——修好的值被主导源 NULL/旧值逐行盖回，单测全绿但生产无效；修法=修复前强制枚举该字段全部写入路径，验收必须看主导写源的落库行
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [事实核验, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-434
  - type: commit
    ref: 1686666f
  - type: doc
    ref: docs/D-434复审R1返修-okx事件流buffer三层回落补齐-2026-09-24-Devin.md
  - type: server_fact
    source: postgres
    query: SELECT src, buffer_multiple, fetched_at FROM fin_venue_account_snapshot WHERE venue='okx' ORDER BY fetched_at DESC
    observed_at: 2026-09-24T01:31:00Z
---

## 事实/症状

D-434 P1：REST `accountOKX` 的 OKX buffer 三层回落修复正确、单测全绿、已部署——但生产 `fin_venue_account_snapshot.buffer_multiple` 持续 NULL。取证发现 src 分布：okx_event（WS 推送，5s/行，2h 1435 行）vs scheduler（REST，2h 23 行）；WS 路径 `okxAccountPushRow` 与修复前 REST 同病（只读顶层 `mgnRatio`），主导源高频盖回，REST 修好的行瞬间被 NULL 覆盖。

## 根因或策略解释

字段级缺陷的排查只沿「消费面→读取面」trace，漏了「生产面」——同一字段有多个写入路径（scheduler/REST、WS 事件流、回填、迁移），每条路径独立解析同一上游数据，缺陷代码往往在多条路径上同源复制。单测只验证被修路径的函数行为，无法暴露未修路径的盖回；只有按 src 分组的落库取证能区分。

## 处置

①设计/施工前：对目标字段 `grep` 全部 INSERT/UPDATE 点，枚举写入路径清单（含事件流、回填、测试基建），同类缺陷逐路径核对；②修法首选共享提取（`reconcile.OKXBufferMultiple` 单源，REST/WS 同调）防再次漂移；③复审部署取证必须按写源分组查最新落库行（主导源行非空才算生效），不能只看修复路径的单元测试。

## 证据

- check: `SELECT src, buffer_multiple, fetched_at FROM fin_venue_account_snapshot WHERE venue='okx' ORDER BY fetched_at DESC LIMIT 10`
- result: 部署边界翻转——src=okx_event 行 01:30:46 前全 NULL → 01:31:05 起 buffer_multiple=86.11~86.14 持续落库
- ref: D-434 / merge@1686666f / LOG「2026-09-24 复审收编交付」

## 反证条件

- check: 字段只有一个写入路径，或各写源共用同一解析函数（本已共享提取）
- falsifies_when: 主导源落库行仍 NULL/旧值而低频源正确——说明仍有未枚举到的写源，继续枚举而非宣告修复完成

## 适用边界

适用于一切持久化字段的缺陷修复（采集→落库链、快照表、状态列）。对纯内存/单次计算路径不适用；对「写入路径唯一」的字段枚举步骤退化为确认唯一性。
