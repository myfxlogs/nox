---
schema_version: 2
id: host-pg-occupies-5432-container-on-5433
kind: pitfall
risk: medium
status: verified
keywords: [5432, 5433, 端口映射, 宿主级 PG, 容器 PG, 测试 DSN, password authentication failed]
---
# 宿主机 5432 被 PG 占用，目标容器库在 :5433——连接报 auth 失败是假象

## 症状
从宿主机连 `localhost:5432` 访问容器化 PostgreSQL，报 `password authentication failed for user "arb"`；换 `.env`/容器 env 里的任何已知密码均失败，极易误判为「密码轮换过/凭据过期」开始排查凭据链。

## 根因
宿主机自身跑着一个 PG（`ss -ltnp` 5432 归宿主 postgres 进程）；目标容器库的端口映射是 `5432/tcp -> 127.0.0.1:5433`。连 5432 连到的是**另一个数据库**，用户/密码当然不对。`docker port <容器>` 与 `docker exec <容器> psql`（容器内 socket，trust 放行）能立即区分。

## 修法
- 测试/运维 DSN 一律走 **:5433**：`postgres://arb:<pw>@localhost:5433/<db>?sslmode=disable`
- 排障顺序：`docker port` → `ss -ltnp | grep 5432` → `docker exec psql` 三步定位，勿先怀疑凭据

## 证据
2026-09-06 复审 D-245 时三连败（.env 密码/容器 POSTGRES_PASSWORD/core DB_DSN 密码全拒），`docker port arb-postgres` 显示映射 5433，改端口即通（arb_test_review_claude，pgstore 全包测试 16.4s 绿）。

## 反证条件
`docker port` 显示目标容器映射就在 5432 时本条不适用（此时 auth 失败才是真凭据问题）。
