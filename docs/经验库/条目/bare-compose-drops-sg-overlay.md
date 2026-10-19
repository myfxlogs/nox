---
schema_version: 2
id: bare-compose-drops-sg-overlay
kind: pitfall
risk: high
status: verified
keywords: [docker compose, sg 覆写, -f 漏带, timescaledb, $libdir, 全 DDL 死, 生产停机]
scope: [deploy, sg, compose, incident]
summary: SG 裸跑 compose（漏 -f docker-compose.sg.yml）按基座 postgres:16 重建 pg→tsdb extension 在册但 .so 缺席→event trigger 拦死全 DDL→core migrate 崩环；信号=$libdir/timescaledb 报错+inspect Image=postgres:16；修法=双 -f 全带重建回薄镜像
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## 裸 compose up = 覆写层丢 → pg 回 vanilla + 全 DDL 死（生产事故）

- **日/席**：2026-10-19 / 决策席
- **触发**：SG 上某会话以 `docker compose -f docker-compose.yml up -d`（**漏 `-f docker-compose.sg.yml`**）重建容器→`nox-postgres` 被按基座 `image: postgres:16` 重建为 vanilla；timescaledb extension 目录记录 2.30.2 在但 .so 文件无→ts 事件触发器拦死**一切 DDL**→core migrate `create schema_migrations` 直接 ERROR，core 崩环 ~34min。
- **修复**：`docker compose -f docker-compose.yml -f docker-compose.sg.yml up -d` 重建回薄镜像 `nox-postgres:16-tsdb-2.30.2`（镜像在案未丢、数据卷未动）→migrate 过闸+写连续恢复。
- **判定信号**：core 日志 `migrate: ... could not access file "$libdir/timescaledb-..."`=「extension 在册、.so 缺席」=镜像被换回 vanilla 的铁证；`docker inspect nox-postgres -f '{{.Config.Image}}'` 秒断。
- **同族**：「部署先于并入」的姊妹病灶——覆写层文件在 master/SG 都齐，但只要一次裸 compose（或脚本/cron 漏 -f）就静默回退。**SG 上一切 compose 命令必须双 -f 全带**；脚本化件统一走 `scripts/`，禁手敲 compose。
- **等级**：high（生产停机级）。
- **出处**：settle 单消费者管线复审 session（注记65）。
