---
schema_version: 2
id: test-dsn-host-scram-stale-env-throwaway-pg
kind: pitfall
risk: medium
status: verified
keywords: [ARB_TEST_PG_DSN, scram, trust, pg_hba, .env, DSN 失效, 只读账号, arb_ro, docker, 一次性容器, postgres:16, 四测试库, CI 面, 5439]
scope: [Go 测试, DSN 门控测试, docker 化 postgres, 本机施工/复审环境]
summary: .env 里 DSN 账号（只读类）密码过时+pg_hba 仅 loopback trust——容器内 `docker exec psql@127.0.0.1` 走 trust 全通（凭据错误被掩盖成"连通 OK"），宿主机/跨容器网段走 scram 即 password authentication failed；跑 DSN 测试别再考古凭据，直接起一次性 postgres 容器+按 CI 四库配方建库（arb_test/arb_exec_test/arb_service_test/arb_reconcile_test），测毕即焚
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: [验证与交接, KB维护]
evidence_refs:
  - type: commit
    ref: 69f22571
  - type: doc
    ref: .github/workflows/ci.yml（集成面①-④ DSN 配方）
  - type: doc
    ref: docs/handoff/LOG.md「D-350R2 返修三残 施工」段环境注记
---

## 事实/症状

- 施工/复审要跑 DSN 门控测试（`ARB_TEST_PG_DSN` 等 4 变量）时发现宿主机无可用凭据：`/opt/arb/.env` 唯一 DSN 是只读类账号（arb_ro），宿主机→docker-proxy（127.0.0.1:5433）与跨容器网段均报 `FATAL: password authentication failed`。
- 陷阱在验证方法本身：`docker exec arb-postgres psql "postgres://<user>:<pw>@127.0.0.1:5432/..."` 显示连通 OK——pg_hba 对 loopback（127.0.0.1/::1）与 local 全 trust，**任何错密码都"通过"**，造成"凭据没问题"假象；换宿主机/跨容器 IP 即 scram 现形。
- 无 sudo、原生 5432 实例 peer 拒绝，凭据考古（bash history/env/memory/KB）全空。

## 根因或策略解释

pg_hba 的 trust 规则只覆盖 loopback/local，其余网段 scram-sha-256；`.env` 的 DSN 账号密码已轮换/失效（且属只读 AI 账号，与 `ops-script-dsn-permission-mismatch` 同族）。容器内 loopback trust 让最方便的"验证连通"路径系统性掩盖凭据失效。

## 处置

放弃凭据考古，起**一次性测试 postgres 容器**（不触生产实例）：

```
docker run -d --name arb-r2-test-pg -e POSTGRES_PASSWORD=test \
  -e POSTGRES_USER=postgres -p 127.0.0.1:5439:5432 postgres:16
# 按 CI 面建四库：arb_test / arb_exec_test / arb_service_test / arb_reconcile_test
# DSN 例：postgres://postgres:test@127.0.0.1:5439/arb_test
# dashboard 面③：ARB_TEST_PG_DSN 与 ARB_TEST_PG_SERVICE_DSN 同指 arb_service_test
# 共库面①：-p 1 串行（见 shared-testdb-120s-lock-flake-isolated-green）
```

测毕 `docker rm -f` 即焚，零生产实例变更。

## 证据

- check: 宿主机 `psql "postgres://arb_ro:<pw>@127.0.0.1:5433/arb_test"`（pw 取自 .env）→ password authentication failed；同凭据容器内 loopback → OK（trust 掩盖）
- check: 一次性容器按 CI 四库配方跑 R2 受影响包全量面+race 三包 → 全绿（commit 69f22571 机检行）
- ref: seat/zcode-d350r2 @69f22571；.github/workflows/ci.yml 集成面①-④

## 反证条件

- check: 若运维恢复统一测试凭据（宿主网段 scram 可用）或 pg_hba 收紧后本条处置仍必要 → 条目不失效；若 .env DSN 在宿主机直连成功
- falsifies_when: 宿主机用 .env 凭据直连测试库成功且成为常态入口

## 适用边界

- 仅本机 docker 化 postgres 部署形态；生产实例本身零变更（容器一次性、独立端口）；不适用于 CI（已有专用 runner 配方）。
