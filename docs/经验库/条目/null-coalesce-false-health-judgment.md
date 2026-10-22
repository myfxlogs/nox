---
schema_version: 2
id: null-coalesce-false-health-judgment
kind: pitfall
risk: high
status: observed
keywords: [NULL, COALESCE, 判据, 假健康, fail-closed, buffer_multiple, idle_floor_streak, 永假健康]
scope: [判据面, 仪表, dashboard, 比较判据设计]
summary: NULL 输入经 COALESCE 默认值进比较判据=永假健康：BelowFloor=BufferMultiple>0 && <1，buffer_multiple 全量 NULL→COALESCE 0→首条件恒 false——指标自出生不可能非零，仪表永久假健康；判据输入缺席须走显式不可判分支
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳-分析员.md（行212，眠钱瓦片永假健康零批；SG 实拍 10 池全 NULL、idle_floor_streak 恒 Days=0）
---

## 现象

`BelowFloor = BufferMultiple > 0 && < 1` 判据+`COALESCE(buffer_multiple,0)`：
输入全量 NULL → 首条件恒 false → 指标自出生不可能非零 → 仪表永久假健康
（SG 实拍 10 池全 NULL、idle_floor_streak 恒 0 天）。

## 判据设计律

- 比较判据的输入缺席必须走**显式不可判分支**（fail-closed/unknown），禁经
  默认值静默落「未触发」侧——默认值让缺席与真值不可判。
- 判据输入要有「**至少一次非零实证**」才算活指标——永假指标与无指标同害
  更恶（占读位且粉饰）。

## 适用边界

一切读面判据/闸值比较通用；同族=读败→零值直填（断供冒零坑姊妹条
missing-available-flag-fake-zero）。
