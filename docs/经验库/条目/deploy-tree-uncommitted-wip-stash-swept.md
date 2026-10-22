---
schema_version: 2
id: deploy-tree-uncommitted-wip-stash-swept
kind: pitfall
risk: high
status: observed
keywords: [git stash 扫走, 主树未提交 WIP, 并发席位腾树, stash^3 未跟踪恢复, worktree 纪律]
scope: [deploy-tree, git, 多席并发, 施工件安全]
summary: 主树 /opt/nox 未提交施工件被并发席位「腾树合并」git stash 整体扫走——10-19 实证 6 文件+未跟踪件卷入 stash@{0}，源文件从工作面消失如被回滚；恢复径=stash 三父
first_seen: 2026-10-19
last_verified: 2026-10-21
skills: []
evidence_refs:
  - type: doc
    ref: docs/经验库/待归纳.md（行183 实证）
---

## 现象

并发收口席在主树 `git stash` 腾树合并——本席在主树误施的未提交 WIP（6 文件改动
+未跟踪件）被卷入 stash@{0}，工作面源文件消失如被回滚。

## 恢复径（stash 三父）

- tracked 改动：`git stash apply stash@{N}`；
- 未跟踪件：`git checkout stash@{N}^3 -- <path>`（stash 第三父=untracked 集）。

## 判据（纪律根件）

开工即建 wip worktree、主树零施工（§4 本有，违者即踩）——与
git_push.sh-worktree-refspec 坑同族（部署树/并发面操作伤）。
