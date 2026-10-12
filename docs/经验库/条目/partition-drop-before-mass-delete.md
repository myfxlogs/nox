---
schema_version: 2
id: partition-drop-before-mass-delete
kind: pitfall
risk: medium
status: verified
keywords: [Postgres, 分区表, 大 DELETE, 死元组, 表膨胀, VACUUM, 磁盘治理]
scope: [ops]
summary: 分区表历史数据清理先 DROP 旧分区再 DELETE 增量——直接全量 DELETE 产生巨量死元组+WAL 膨胀，autovacuum 滞后致表膨胀反噬磁盘目标
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: []
evidence_refs:
  - type: decision
    ref: D-213 审计 M1（docs/D-213-215审计报告-2026-09-04.md）
---

## 事实/症状

D-213 ticks 保留 72h 设计稿初版「DROP 08 分区 + DELETE 72h 外」并列无序。审计实测 >72h 存量 = **4,004,524 行**（全库 6.99M）——若先跑 DELETE（或只 DELETE），一次性 400 万行删除产生 ~2GB 死元组+等量 WAL 峰值，autovacuum 对大表触发阈值滞后（默认表大小 20%+50 行）→ 空间迟迟不回收甚至表膨胀，磁盘治理目标被反噬。

## 根因或策略说明

DELETE 是逐行标记死元组（空间表内复用、不还 OS、靠 VACUUM 整理），DROP PARTITION 是文件级删除（即时还 OS）。历史分区数据整段无用时，DROP 是唯一 O(1) 路径；DELETE 只该用于最近分区的增量边界。

## 处置

顺序硬约束：①DROP 最旧分区（即时还盘，消掉存量大部分）②对剩余分区按保留窗 DELETE（此时量级 ~30 万行可控）③DELETE 后显式 `VACUUM ANALYZE`（不等 autovacuum）。每日 cron 保留任务同构：每日只删一天增量+VACUUM，稳态有界。

## 证据

- 实测：`count(*) FILTER (WHERE ts < now()-72h)` = 4,004,524；执行实录 DROP 08（df 80%→76%）+ DELETE 134,329 行 + VACUUM + retention 首跑 `deleted 183 rows`
- 生产：ticks 1,035MB→稳态 ~450MB 封顶

## 反证条件

需要保留跨分区细粒度数据（保留窗不在分区边界上且不能 DROP 整分区）时只能 DELETE——此时必须分批（LIMIT 批次循环）控制单事务死元组量。

## 适用边界

RANGE 分区+按时间保留策略的表（ticks/日志类）。非分区表不适用 DROP 路径。
