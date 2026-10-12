---
schema_version: 2
id: snapshot-id-vs-entity-id
kind: pattern
risk: high
status: verified
keywords: [组合键, 快照ID, 实体身份, 去重, 时间戳后缀, 反去重, 同组合叠仓, upsert, DedupKey, 业务键]
scope: [data-model, execution-guard]
summary: 快照级唯一 ID（含时间戳后缀）当实体主键=结构性反去重——99.5% 冗余行、同实体重复执行无防、历史不可聚合；实体身份必须是业务键，快照键仅作历史归档
first_seen: 2026-08-22
last_verified: 2026-08-22
skills: []
evidence_refs:
  - type: decision
    ref: D-080 机会组合键贯通与验证闭环
  - type: doc
    ref: docs/机会组合键贯通与验证闭环方案.md
---

## 事实/症状

机会 ID=组合+unix_ms（每 tick 唯一）。三个后果实证：① 近 24h 落库 24,525 行、去重后 118 组合，99.5% 冗余，表无限增长；② gate 去重键=tick ID，挡不住同组合新 tick → #264/265/267 同组合 7 分钟 3 次开仓（风控缺口）；③ 无法按组合聚合历史（承诺 vs 实现对账断裂）。

## 根因或策略解释

身份错位：机会的「实体」是组合（TYPE:CANONICAL:v1:v2，持久），「快照」是某时刻的指标值。把快照唯一性（时间戳后缀）当实体身份 → 每次观测都产生新实体 → 去重在结构上不可能，任何「同实体」检查（防重/冷却/聚合）都要先人工剥时间戳。

## 处置

① 主键=业务实体键，同实体新观测 upsert 原行（D-80：opportunities 组合键化）；② 旧快照表 rename 归档保历史，不清理；③ 新旧格式兼容解析：尾段纯数字才剥离（venueType 永非数字）；④ 同实体执行防重独立成守卫（combo→OPEN 集），不依赖 ID 唯一性；⑤ 聚合/审计表（rollup）按实体×日，喂累积于观测路径（write-behind，零定时器）。

## 证据

- check: `count(*) vs count(distinct 前6段)` 近 24h = 24525/118；positions #264/265/267 同组合三开
- result: 日增 2.4 万行 → 组合量级（修复⑬验收项）
- ref: D-080 / 修复⑬施工提示词

## 反证条件

- check: 修复⑬ 部署 24h 后 opportunities 行数与 rollup 日增
- falsifies_when: 若按组合键 upsert 后仍线性增长（说明仍有快照键写入路径漏改）

## 适用边界

任何「周期观测产生行」的表（机会/信号/告警快照）。不适用：观测本身就是要留档的审计流（那类该 append-only，但主键仍应是事件 ID 而非实体 ID，两者分开）。
