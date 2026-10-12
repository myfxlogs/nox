---
schema_version: 2
id: compose-service-name-cross-project-conflict
kind: pitfall
risk: medium
status: observed
keywords: [docker compose, 容器名冲突, project 名, arb-postgres-test, 全量 up, d021-test-pg, Conflict, already in use]
scope: [部署, docker compose, 测试实例 arb-postgres-test, arb 主 compose]
summary: 主 compose 内定义的服务若曾以独立 project 名（如 D-021 用 -p d021-test-pg 起 arb-postgres-test）在跑，则主 project 的全量 `docker compose up -d --build` 会因同名容器被外 project 占用而中断（arb-core 不重建、静默跑旧代码）；规避=部署命令带服务名 `up -d --build arb-core`，root fix=容器迁回主 project 或 compose 服务挪独立 profile
first_seen: 2026-10-04
last_verified: 2026-10-04
skills: [部署]
evidence_refs:
  - type: decision
    ref: D-021（测试实例以独立 project 起）
  - type: server_fact
    source: docker
    ref: 368f8932b1bc arb-postgres-test project=d021-test-pg Up 37h healthy（2026-10-04 实拍）
  - type: decision
    ref: D-060 复审部署实录 2026-10-04
---

# compose 服务名跨 project 容器名冲突：全量 up 静默不部署

## 症状

在 /opt/arb 执行契约标准部署命令 `docker compose up -d --build` 报：
`Conflict. The container name "/arb-postgres-test" is already in use by container 368f8932b1bc`，
且中断处 arb-core 只打印 `Recreate` 未实际重建——**部署静默失败，容器继续跑旧代码**（本次实拍 arb-core 仍是 45 分钟前的旧容器）。

## 根因

D-021 施工期测试实例用独立 project 名（`docker compose -p d021-test-pg up`）起了 `arb-postgres-test`，之后主 compose 文件也加入了同名服务定义，但主 project（arb）从未真正启动它——一直靠 d021-test-pg 名下的容器服务 5434（healthy 37h）。历次部署因带服务名（`up -d --build arb-core`）未触发；一旦全量 up，主 project 尝试 create 同名容器即撞名。

## 修法

- **即时规避**：部署命令恒带服务名 `docker compose up -d --build arb-core`（契约 §8 的全量形态在双 project 并存期间会炸）；
- **root fix（未做，2026-10-04 记）**：择一收口——外 project 容器删除后由主 project 接管（volume 前缀随 project 变，测试库数据可弃），或 compose 把 test 服务挪独立 profile（`--profile test` 显式启停）。做之前每次全量 up 必复发。

## 例外

带服务名的部署不受影响；删除外 project 容器前须确认无测试在跑（5434 正被使用时删=断测试库）。
