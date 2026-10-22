---
schema_version: 2
id: deploy-image-tag-mismatch
kind: pitfall
risk: high
status: observed
keywords: [镜像名错位, nox-core tag, arb-core tag, docker inspect, 假部署, compose build, --network=host]
scope: [deploy, docker, SG, 跨仓习惯带错镜像名]
summary: 按 arb 仓化石契约构建+推送 arb-core:latest——NOX 钦定 nox-core:latest，三次 up -d 全拉回旧镜像=三次假部署（D-630 返修/尾巴/venue-gap 未上线、迁移 1059 未跑）；镜像在仓库躺着服务纹丝不动，唯赖 compose build 段兜底
first_seen: 2026-10-20
last_verified: 2026-10-21
skills: [生产取证]
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳.md（行180+行197 同坑双实证）
---

## 现象

跨仓习惯带错镜像 tag：`docker build -t arb-core:latest` + push，NOX compose 跑
`nox-core:latest`——`up -d --force-recreate` 三次全拉回旧 nox-core 镜像=**三次假部署**
（修复件未上线、迁移未跑）。另实证：镜像 push 到 SG 但服务不重启=同样没部署。

## 判别锚（验收硬件）

- 部署后 `docker inspect nox-core --format {{.Config.Image}}` 必须==刚载镜像名
  （`{{.Image}}` 含 sha——与构建 manifest 对拍）；
- 「docker load/build 成功」≠部署成功；
- 迁移类部署另锚 `migrate_table version==新增迁移号`。

## 修法

部署脚本径钦定：`docker build -t nox-core:latest -f Dockerfile.core .`；
`--network=host` 必带（mihomo 代理下裸 NAT 拉包 ECONNRESET 实证）。
