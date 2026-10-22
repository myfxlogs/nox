---
schema_version: 2
id: missing-available-flag-fake-zero
kind: pitfall
risk: high
status: observed
keywords: [读败冒零, available 旗, proto 标量, optional, TotalEquityUsd, nav $0.00, 断供, 仪表失真]
scope: [仪表, proto 契约, dashboard, 对外数值字段]
summary: 「读败→零值直填非 optional 字段」=断供冒零通用形态：equityTotalUSD err→return 0,0,nil→TotalEquityUsd:0（无 available 旗）→前端 nav!=null 恒显 $0.00；新增对外数值字段时 available 旗必须同票落
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳-分析员.md（行216，总览瓦片第1性审计批）
---

## 现象

equityTotalUSD 读败 `return 0,0,nil` → `TotalEquityUsd:0`（proto 标量无配对
available 旗）→ 前端 `nav!=null` 恒显 $0.00——断供与真零在渲染面不可判，
业主看到「正常零值」而非「数据缺失」。

## 判别锚（验收硬件）

- 新增对外数值字段时 `available`/`as_of` 旗必须**同票落**（别等审计翻出）；
- proto 标量字段默认零值无法区分「缺数据」与「真零」——契约期不设旗=后期
  无正解路径（改线型=breaking）。

## 处置

读败禁直填零值：要么 available 旗显式降级（前端显「—」非 $0.00），要么
fail-closed 不可判分支。

## 适用边界

一切 proto→前端数值透出通用；姊妹条=null-coalesce-false-health-judgment
（判据输入缺席的判定侧同形病灶）。
