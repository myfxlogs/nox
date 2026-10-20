---
schema_version: 2
id: backup-r2-silent-degrade
kind: pitfall
risk: high
status: verified
keywords: [备份, R2, backup-r2.env, 静默降级, D-110]
scope: [backup, sg, observability]
summary: R2 外推 env 缺失仅 log WARN 零告警→三地备份实质单地静默八晨；信号=backup.log WARN+lsd 403
first_seen: 2026-10-13
last_seen: 2026-10-20
---

## 备份腿断裂静默降级——WARN 只进 log 不进告警面（数据损失暴露级）

- **日/席**：2026-10-20 / 复审席（零开仓事故修复方案文档独立审计中发现）
- **实证**：SG `/opt/nox-backups/backup.log` 全程 28 行，`2026-10-13T03:00Z` 至 `2026-10-20T03:00Z` **八晨连续** `WARN: /opt/nox-backups/backup-r2.env missing/incomplete, degrade to local-only backup`；`rclone lsd r2:nox-backup`=403 AccessDenied；本地滚动=8 份（RETENTION_DAYS=7+14 份硬顶）。即 D-110 三地备份自本机部署起第二落点（R2 age 加密）**从未成功过一次**，安全垫实质=单地 8 份。期间零 alerts 行、零飞书——脚本 `emit_alert` 只覆盖 pg_dump 失败支，不覆盖降级支。
- **判定信号**：`grep WARN /opt/nox-backups/backup.log`；`ls /opt/nox-backups/backup-r2.env`；`rclone lsd r2:nox-backup`。
- **同族**：「观测滞后」欠账家族（零开仓事故三类系统性欠账之一）——凡降级/fail-open 支必须带系统级可见面（alerts+飞书），log-only=对业主不可见=等于没有。与 D-033（系统自盯）同根。
- **修法方向（决策席另票）**：①按 AGENTS §8 落 `backup-r2.env`（AGE_PUBKEY/R2_REMOTE/R2_BUCKET，600，经 `scripts/backup_r2_test.sh` 生成+端到端验）；②降级支接 `emit_alert` critical（同 pg_dump 失败支语义）；③digest/cron 面加 R2 对象存在性周期核（防「env 在但上传静默败」变体）；④VPS 迁移票前置条件必含本项（迁移安全垫声明必须实拍对账，禁「三地齐」口惠）。
- **等级**：high（数据损失暴露级：SG 单盘损=全量数据仅剩 GitHub 代码面，DB 零异地副本）。
- **出处**：`docs/零开仓事故修复方案.md` §10 独立审计 E4/10.5。
