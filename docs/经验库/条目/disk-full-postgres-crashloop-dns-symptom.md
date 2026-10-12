---
schema_version: 2
id: disk-full-postgres-crashloop-dns-symptom
kind: pitfall
risk: high
status: verified
keywords: [磁盘满, postgres 崩溃循环, DNS 解析失败, e2e 假性失败]
scope: [ops, workflow]
summary: 根盘满→postgres 崩溃循环，core 表象却是 DNS 解析失败；e2e 失败数跑次波动+两臂同败=环境信号，先查容器/磁盘再归因代码
first_seen: 2026-08-22
last_verified: 2026-09-26
skills: []
evidence_refs:
  - type: server_fact
    ref: docker logs arb-postgres「FATAL：could not write lock file "postmaster.pid"：No space left on device」+ df 100%
    verified: 2026-08-22
  - type: decision
    ref: D-082 关联留痕（LOG 2026-08-22 磁盘事故处置）
---

# 磁盘满 → postgres 崩溃循环 → DNS 表象 + e2e 假性失败

## 症状

- 前端弹「连接错误：failed to connect … hostname resolving error: lookup arb-postgres on 127.0.0.11:53: no such host」——像 Docker 网络/DNS 问题。
- `docker ps` 显示 `arb-postgres Restarting (1)`，`docker logs` 真因是 `FATAL: could not write lock file "postmaster.pid": No space left on device`，`df -h /` = 100%。
- 全套 e2e 从零星失败恶化到大面积失败，且**同代码两轮失败数不同**（8→12 failed）——不稳定是环境信号不是代码信号。

## 坑

1. DNS 报错是**误导表象**：core 的 pgx 连接失败发生在容器重启窗口，错误信息取的是解析期错误；真因在 postgres 容器日志与磁盘水位。
2. e2e 归因陷阱：重构/大改动在途时极易把这类失败误判为回归。判别法：① 失败数跑次间波动；② 控制实验两臂（改动前/后）同败；③ error-context 快照里找后端连接错误而非元素缺失本身。

## 修法与预防

- 止血：`docker builder prune -af`（build cache 常年数 GB 且 0 active）+ `journalctl --vacuum-size=200M` + /tmp 清理；postgres 随下一轮 restart 自愈，数据完好（小盘机器 go-build 缓存可另占数 GB）。
- 预防：磁盘用量告警（>85% 入 alerts）；journald 持久收缩（SystemMaxUse）属审计可观测性变更，需业主批准。
- 诊断顺序：`docker ps` 容器状态 → `docker logs` 真因 → `df -h`/`free -g` → 再谈代码。

## 证据

- 2026-08-22 重现: disk-full 崩溃循环+两臂 e2e 同败 | ref: docker logs arb-postgres 12:22 UTC 起
- 2026-08-22 重现: 清理 4.5G 后 postgres healthy，e2e 残留失败全部 git 史实证为既有过时断言 | ref: LOG 2026-08-22 会话纪要
- 2026-08-25 二次事故（新变体）: 元凶=/root/.cache/go-build 7.7G（当日高频构建积累），表象=公网 502（Cloudflare Tunnel 后端无响应）而非 DNS；postgres PANIC "No space left on device"（checkpoint 写 pg_logical 失败）→ 恢复循环 → core 崩循环。处置=go clean -cache+docker prune 释放 8.2G→自愈。防护升级=ops_disk_guard 阈值 85→80+自愈清单加 go clean -cache 与 docker image prune | ref: STATE 施工表 D-104 P2 行
