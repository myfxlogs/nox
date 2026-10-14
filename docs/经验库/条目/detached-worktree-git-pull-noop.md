---
schema_version: 2
id: detached-worktree-git-pull-noop
kind: pitfall
risk: high
status: verified
keywords: [worktree, detached HEAD, git pull 空转, 镜像构错提交, 部署错版]
scope: [部署, worktree, docker build, 发布流程]
summary: detached HEAD worktree 上 `git pull --ff-only` 是空操作（无上游分支可 ff）——镜像构在旧提交照常部署成功，生产跑的是过期代码；部署链路必须 `git rev-parse HEAD` 核源而非信 pull 成功。
first_seen: 2026-10-14
last_verified: 2026-10-14
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: "SG 2026-10-14：/tmp/nox-deploy worktree detached@0efa077，pull 空转后镜像构在 7e49e344——订单 #24 仍走旧 min() 划转语义再败一次"
---

# detached worktree 部署坑：`git pull` 空转 → 镜像构在旧提交

## 病灶链

部署为「不带在飞 WIP」用干净 worktree 构镜像：`git worktree add /tmp/x <sha>` → HEAD 是 **detached**。下一次部署惯性敲 `cd /tmp/x && git pull --ff-only` ——detached HEAD 无上游分支，pull **零输出零报错零生效**，worktree 钉死在旧 sha。`docker build` 照常成功、`docker save` 照常推、容器照常起——**生产跑的是旧代码，全程无任何失败信号**。

## 判据

部署链路「源码版本」必须显式钉：worktree 路径部署 = `git fetch && git checkout <目标sha>`（或 `worktree add -f` 重建），**禁 `git pull`**。构镜像前 `git rev-parse HEAD` 打日志/进交付记录；部署后首拍行为若与代码预期不符，第一对照就是「容器里跑的 commit」与「刚提交的 commit」。

## 同族信号

- 「改了代码部署了但行为没变」——先核镜像源 commit，再怀疑代码没生效。
- worktree 复用省事是诱因：`worktree add` 的目录天然 detached，跨会话复用必带显式 checkout。
