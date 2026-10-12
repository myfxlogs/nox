---
schema_version: 2
id: git-commit-timezone-vs-runtime-utc
kind: pitfall
risk: medium
status: verified
keywords: [git, 时区, 部署范围, docker, StartedAt, 复审状态, UTC, +0330]
---
# git commit 时间戳时区 ≠ 运行时 UTC——部署范围核对先统一时区

## 症状
核对「哪些 commit 已上生产」时，git log 时间戳显示晚于容器启动时间（docker inspect State.StartedAt 为 UTC），误判「未复审代码被搭车部署」，引发假警报与错误流程结论。

## 根因
git 作者/提交时间戳带本地时区偏移（本项目环境 +0330），而 docker inspect StartedAt、容器日志时间戳均为 UTC。两侧直接字符串/数值对照而不换算，差 3.5h 的系统性偏移。

## 修复/判定
对照前统一换算 UTC（commit +0330 → 减 3h30m）。实证案例（2026-08-30）：部署点 16:25 UTC，Symbol 分类 commit 19:51+0330=16:21 UTC——早 4 分钟，在部署内；若不换算则误判为「部署后代码进了生产」。

## 预防
- 部署范围核对清单第一步：`git log --format="%ci %h"` 输出显式带偏移，换算后再与 StartedAt 对照。
- 涉及「复审状态 vs 部署时序」结论前，先做一次 commit-UTC 化对表（本案例修正了 6 个任务的状态判定）。
