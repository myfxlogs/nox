---
schema_version: 2
id: migration-fossil-port-hidden-forward
kind: pitfall
risk: high
status: verified
keywords: [化石端口, 5433, DB_DSN 硬编码, 迁移拓扑漂移, 临时转发, xasset_monitor 停更, docker port, 表格新鲜度]
scope: [deploy, scripts, ops, postgres, migration]
summary: 迁移后宿主脚本仍写死旧拓扑端口（5433），靠迁移期临时端口转发兜底呈假绿；转发断流即全灭——端口类坐标必须取运行时权威值（docker port/docker exec），禁写死
first_seen: 2026-10-07
last_verified: 2026-10-07
skills: [生产取证]
evidence_refs:
  - type: decision
    ref: D-114
  - type: commit
    ref: b387343d3
  - type: server_fact
    source: xasset-monitor-log
    query: "15:45:01 起每 5min connect pg 127.0.0.1:5433 connection refused；15:40 仍成功=转发兜底断流时刻；ss -tlnp 5433 无监听（alphaforge docker-proxy 消失）"
    observed_at: 2026-10-07T15:45:01Z
---

## 事实/症状

xasset_monitor 停更告警（21min 无写入）。追查：`xasset-monitor-run.sh` 等
8 处宿主脚本内嵌 `DB_DSN=...@127.0.0.1:5433` ——5433 是**旧主机**生产 pg
坐标；SG 迁移后生产 pg=15433、5433 归 alphaforge-postgres。但迁移当天
15:40 前这些脚本**一直假绿**——5433 上存在一条迁移期遗留的临时端口转发
（疑似 ssh -R/socat 类兼容通道）把流量转给了正确实例，转发断流后全部
`dial tcp 127.0.0.1:5433: connection refused`。

## 根因或策略解释

- 迁移只改了**显式传参**的调用点（crontab 里 backup/digest 传了 15433），
  **脚本内嵌默认值**全部漏改——`grep crontab` 看不到内嵌 DSN。
- 临时兼容转发把「配置错误」掩盖成「运行正常」——**转发一断，故障才显形**，
  且断流时刻与部署动作相邻易被误判成部署回归。
- 同型教训=D-538（BINANCE_PROXY_URL 化石端点）：凡写死的部署坐标都会变成
  未来的坑。

## 处置

- **宿主侧 psql**：一律 `docker exec arb-postgres psql -U arb -d <db>`——
  与 daily_snapshot.sh/evidence_pos.sh 同款惯例，与端口映射彻底解耦。
- **必须 TCP DSN 的消费者**（Go 工具/pgdump/credread）：
  `docker port arb-postgres 5432 | awk -F: '{print $NF}'` 取运行容器发布值，
  解析失败=fail-closed 报错退出，禁静默默认回退。
- **迁移收口检查表**：端口/主机类坐标改完，必须 `grep -rn "旧值" scripts/ tools/ bin/`
  清全量内嵌点，不是只改 crontab 显式参；临时转发下线前先全量 grep。
- 部署拓扑变化（compose override 端口映射）是「真相源漂移」——脚本应引用
  运行时拓扑而非字面量。

## 证据

- check: `ss -tlnp | grep 5433`（宿主无监听）+ `docker port arb-postgres`
- result: 5433 无监听者=refused；`docker port` 返回 15433=权威值
