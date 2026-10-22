---
schema_version: 2
id: sg-disk-pressure-map
kind: ops
risk: medium
status: verified
keywords: [磁盘使用率, disk, 77%, ops_disk_guard, docker prune, pgdata, backups, SG]
scope: [ops, SG, docker, postgres, backups]
summary: SG 根盘 77% 取证分层图——docker volumes 16G（pgdata 11G 主增长极）+备份/死项目域；常规回收=builder prune -af+tagged 废镜像点名删；死项目域属业主裁
first_seen: 2026-10-22
last_verified: 2026-10-22
skills: [生产取证]
evidence_refs:
  - type: server_fact
    source: postgres
    query: "df -h /; du -xh --max-depth=1 /var/lib/docker/volumes /opt"
    observed_at: 2026-10-22T22:55:00Z
---

## 事实/症状

SG 告警：根盘 77%→自愈（builder>24h prune）后 76% 仍超 75% 阈。

占用分层（58G 盘 / 45G 用）：
- `/var/lib/docker/volumes` **16G**：nox_nox-pgdata 11G（生产 pg——ticks hypertable 主增长极）、ant_postgres_data 2.9G（死项目）、nox-pgdata-test 1.1G（测试卷）
- `/opt` 8.1G：nox-backups 2.8G（日 dump ~340M×13d 滚动，RETENTION_DAYS=13 已封顶）、arb-backups 2.4G（arb 死项目，10-12 末次）、/opt/ant 2.5G（死项目 checkout）
- `/usr` 6.1G、`/root` 3.0G（go 743M+go-cache 251M）

## 根因或策略解释

ops_disk_guard 日跑只管 builder cache>24h——tagged 废镜像（旧版本/死项目）、死卷、死项目备份不在其域，缓慢淤积至阈值。

## 处置

已清（常规面可自主）：`docker builder prune -af` →1.6G + `docker rmi golang:1.24-alpine`（1.26 已取代）→0.4G，76%→**73%** 落阈下。

待业主裁（非 NOX 域/破坏面）：
- arb-backups 2.4G + arb 卷/镜像（备份停 10-12、容器零存活=项目死）
- /opt/ant + ant_* 卷 5.6G（死项目）
- nox-postgres-test Exited 容器+1.1G 卷+postgres:16 镜像 642M（测试台退役则清）
- nox-pgdata 11G 结构性增长——ticks 保留口径治理件（非本次域）

## 证据

- check: `df -h /` + `du -xh` 分层 + `docker ps -a`/`docker system df` 对拍
- result: volumes 16G（pgdata 11G）；清 builder+dangling tagged 镜像后 43G/58G=73%
- ref: 2026-10-22 处置实录

## 反证条件

- check: `docker image prune -f` 预期清 tagged 镜像
- falsifies_when: prune -f 只清 dangling——tagged 废镜像须 `docker rmi` 点名；`docker compose up -d <svc>` 连带 recreate 依赖撞资源限——须 `--no-deps`
