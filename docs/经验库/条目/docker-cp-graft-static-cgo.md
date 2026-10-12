---
schema_version: 2
id: docker-cp-graft-static-cgo
kind: pitfall
risk: high
status: observed
keywords: [docker cp, 嫁接部署, exec no such file or directory, CGO_ENABLED, alpine, glibc, 静态链接, arb-core]
scope: [arb-core, docker, 部署链, alpine 镜像, go build]
fact:
  source: postgres
  query: "docker exec arb-core ls -la /bin/core; file /tmp/arb-core-new"
  observed_at: 2026-09-27T08:30:00Z
  server_revision: "master@716c6631 前后"
  response_hash: ""
---

## 事实/症状

`docker compose up -d --force-recreate` 后 `docker cp` 新二进制入容器、`docker restart` → 容器反复 `Restarting (255)`，日志 `exec /bin/core: no such file or directory`（文件明明存在）。同窗口发现：graft 只写容器层，recreate 会**还原镜像内旧二进制**（graft 件丢失）。

## 根因或策略解释

两层独立坑叠加：
1. **CGO**：宿主机 `go env CGO_ENABLED=1` 默认开——`go build` 产出动态链 ELF（interpreter `/lib64/ld-linux-x86-64.so.2`），alpine 无 glibc loader → exec 报 ENOENT（动态加载器缺失报法，非文件缺失）。
2. **graft 语义**：`docker cp` 改的是容器可写层；`--force-recreate` 以镜像重建容器，graft 件全部蒸发，回到镜像内旧二进制。

## 处置

- 构建部署链一律 `CGO_ENABLED=0 go build`（产出 statically linked，`file` 验）。
- 需要 recreate 的顺序：先从运行容器 `docker cp` 备份现二进制 → recreate → `docker cp` 回灌 → `docker restart`；切勿直接 recreate 以为 graft 件还在。
- 生产机 go env 若需固定：`go env -w CGO_ENABLED=0` 或构建脚本显式钉。

## 证据

- check: `file /tmp/arb-core-new` vs `/tmp/arb-core-prod.bin`
- result: 新件 `dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2`；旧件 `statically linked`——一重即验
- ref: D-515 生产部署 2026-09-27，commit 716c6631 同批处置

## 反证条件

- check: 在 alpine 容器直接 `./core --version`
- falsifies_when: 动态链二进制在目标镜像可执行（镜像含 glibc 兼容层时本坑不触发）

## 适用边界

仅适用于「本地 go build + docker cp 嫁接 alpine 系容器」部署链；CI/镜像内构建（Dockerfile 内 CGO 环境）不受影响。
