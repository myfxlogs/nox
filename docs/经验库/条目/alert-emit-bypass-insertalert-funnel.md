---
schema_version: 2
id: alert-emit-bypass-insertalert-funnel
kind: pitfall
risk: high
status: observed
keywords: [告警漏斗, InsertAlert bypass, 裸 INSERT alerts, created_at vs ts, dev-null-吞错, alertHook, AlertDedupKey]
scope: [alerts, 仓外写入方, cron 脚本, 告警单一漏斗]
summary: 宿主 cron 脚本裸 INSERT INTO alerts 报 SLO=双失——绕 insertAlert 漏斗全钩（feishu 扇出/episode 闸/dedup 归一全失）+列名凭印象 created_at（实列 ts）恒错被 >/dev/null 2>&1 吞成静默零落库
first_seen: 2026-10-20
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳.md（行194 实证——观测 SLO 票 @3aec05295 决策席直裁退回）
---

## 现象（双失）

宿主 cron 脚本裸 `INSERT INTO alerts`：①绕过 insertAlert 漏斗——alertHook
（feishu 扇出/topic 实时推/episode 闸/AlertDedupKey `<N>` 归一 dedup/
alertPaused）全失，「+飞书」验收天然不达；②列名凭印象 `created_at`（alerts
实列=ts；created_at 是 price_alerts 列）恒错——`>/dev/null 2>&1` 吞成静默零落库。

## 修法（判据）

告警单一漏斗=`InsertAlert`/`InsertAlertDedup`——任何仓外写入方（宿主 cron/
外部脚本）须走摄入端点进漏斗不可直插表；脚本 SQL 列名必对 migrations schema
亲证；禁 `>/dev/null 2>&1` 裸吞——至少留失败计数/失败本身成告警。
