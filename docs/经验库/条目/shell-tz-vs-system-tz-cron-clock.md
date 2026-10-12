---
schema_version: 2
id: shell-tz-vs-system-tz-cron-clock
kind: pitfall
risk: medium
status: verified
keywords: [时区, timezone, TZ, cron, 交互shell, harness, Asia/Shanghai, UTC, 误判, 备份]
scope: [运维, cron, backup_pg, digest, 新主机]
summary: 交互 shell 被 harness 强制 TZ=UTC（date 显示 +0000）≠ 系统时区（/etc/timezone=Asia/Shanghai）≠ cron 守护进程时区（随系统=业主钟+8）——以交互 shell 输出推断 cron 触发语义必误判；核证须以 /etc/timezone + 日志内容时间戳 vs 文件 mtime 差值对照
first_seen: 2026-09-14
last_verified: 2026-09-14
skills: [事实核验]
evidence_refs:
  - type: commit
    ref: 008887c3
  - type: doc
    ref: docs/handoff/STATE.md:66
  - type: doc
    ref: docs/handoff/LOG.md（2026-09-14 会话纪要·时区核查更正段）
---

## 事实/症状

- 交互 shell `date '+%z %Z'` 显示 `+0000 UTC`，但同机 `cat /etc/timezone` = Asia/Shanghai、`readlink /etc/localtime` → zoneinfo/Asia/Shanghai——三层时区分裂。
- 据交互 shell 输出推断「cron 表达式按 UTC 触发」→ 误判 backup cron（`0 3 * * *`）「今日未触发」（backup.log 首行 `[2026-09-13T19:00:01Z]` 看似在昨天）。实为业主钟今日 03:00（=UTC 前日 19:00）已正常触发。
- STATE 运维自取「主机时区已改 UTC（9-5 业主令）：cron 数字不变语义变 UTC」是旧主机记录，新主机（9-13 迁移）未沿用——过时记录加剧误判。

## 根因或策略解释

- harness/交互层向 shell 环境注入 TZ=UTC，覆盖系统 localtime——`date` 只反映**会话环境**，不反映系统与 cron 守护进程的环境。
- cron 守护进程不继承交互 shell 的环境变量，随系统 `/etc/localtime` = Asia/Shanghai 触发：digest（`10 6 * * *`）于业主钟 06:10（=UTC 前日 22:10）；backup（`0 3 * * *`）于业主钟 03:00（=UTC 前日 19:00）；snapshot（`17 20 * * *`）于业主钟 20:17。三 cron 均按业主钟本地时触发，与排程意图一致。

## 处置

- 判 cron 语义前必核三处：①系统层 `cat /etc/timezone` + `readlink /etc/localtime`；②会话层 `date '+%z %Z'`（仅代表当前 shell，勿外推）；③进程实证=日志**内容时间戳 vs 文件 mtime 差值**（运行进程的真实时钟）。
- 内容时间 06:10 vs mtime `2026-09-13 22:10:02+0000` → 差 8h → 进程以 Asia/Shanghai 运行 → cron=业主钟，判定闭环。
- STATE 运维自取已更正（commit 008887c3）：标注 9-5「改 UTC」为旧主机 Tehran 根因记录，新主机按 Asia/Shanghai。

## 证据

- check: `cat /etc/timezone; readlink /etc/localtime; date '+%z %Z'`
- result: `Asia/Shanghai` / `Asia/Shanghai` / `+0000 UTC`（三层分裂实证）
- check: digest.log 内容行 `2026/09/14 06:10:02` vs 同文件 `stat` mtime
- result: mtime=`2026-09-13 22:10:02 +0000`（=业主钟 9-14 06:10）→ 运行时钟=+8
- ref: 008887c3（STATE 更正 commit）；STATE.md 运维自取「9-14 核查更正（新主机）」段

## 反证条件

- check: 复核 `/etc/timezone` 是否被改回 UTC，或 crontab/cron 服务配置显式 CRON_TZ/TZ 覆盖
- falsifies_when: 日志内容时间与 mtime（换算后）零差 → cron 已按 UTC 运行 → 本条结论失效并退役

## 适用边界

- 本主机（2026-09-13 迁移新机）harness 交互 shell 场景；其他席位 shell（ZCode/Devin）注入环境可能不同，各席自查自证。
- 不适用于容器内定时任务（容器 TZ 由 compose/env 决定，另核）；本系统 cron 均为宿主机 crontab。
- 仅本项目适用（主机特定配置），不晋升全局 KB——调度器层时区坑已有先例覆盖，主机层以本条为凭。
