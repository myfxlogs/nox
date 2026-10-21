---
schema_version: 2
id: deploy-image-name-mismatch-nox-vs-arb
kind: pitfall
risk: high
status: verified
keywords: [部署, 镜像名, nox-core, arb-core, docker load, compose build, 部署≠并入]
scope: [deploy, production, docker]
summary: "部署镜像名错位=推了等于没推——NOX 契约钦定 `docker build -t nox-core:latest`（AGENTS.md §8）推 SG，compose 服务跑 `nox-core:latest`；按 arb 化石契约建 `arb-core:latest` 载 SG=镜像仓库躺着服务纹丝不动，唯赖 SG 就地 `compose build`（build: 段）兜底未断链"
first_seen: 2026-10-21
last_verified: 2026-10-21
skills: []
---

## 案情

2026-10-21 决策席会话部署链：本机 `docker build -t arb-core:latest`（arb 化石契约沿袭）→ `docker save | ssh 'docker load'` → compose up -d → 容器跑的分明是旧 bundle（`index-Cq_Hy10t.js` 而非新 `index-sCzSsPUP.js`）。实拍钉：`docker inspect nox-core` Config.Image=`nox-core:latest`（SG compose image: 段钦定名），SG 侧 `nox-core:latest` 镜像建于 03:03:55Z——是 SG **就地 compose build**（compose.yml build: 段）从 deploy_sync 同步的源码编的，我载的 `arb-core:latest` 全程是仓库死重。

## 机制

- NOX 正径（AGENTS.md §8 原文钦定）：本机 `docker build --network=host -t nox-core:latest` → save|gzip|ssh load → deploy_sync → compose up -d。`--network=host` 必带（mihomo 代理下 docker 裸 NAT 拉 npm/go 包 ECONNRESET 实证）；镜像名 `nox-core` 非 `arb-core`（arb=前代项目化石）。
- SG compose `build:` + `image:` 双段并存——`compose up` 不自动重建已存镜像，但 `compose build` 会按同步源码就地编：这成了意外的兜底径（源码同步在先，SG 建像在后=生产拿到正确代码，纯属侥幸非设计）。
- 判别锚（部署验收必拍）：`docker inspect nox-core --format {{.Config.Image}}` 必须=刚载镜像名；`curl /` HTML 引的 bundle hash 必须=本机构建产物 hash 对拍。**「docker load 成功」≠部署成功**——容器没换像等于没部署。

## 处置

本坑会话内自纠：SG 就地 `compose build nox-core` 重建（源码已同步含新件）→ 新 bundle hash 产验毕。后续部署一律按 §8 正径 `nox-core:latest` 名建推。
