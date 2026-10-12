---
schema_version: 2
id: self-remote-push-is-zero-offsite-backup
kind: pitfall
risk: critical
status: verified
keywords: [git remote 自环, origin 指向本仓, push up-to-date 假象, 零离机备份, remote -v, ls-remote 核验]
scope: [仓库运维, 多席位 worktree, 部署树, 迁移/历史重写后配置]
summary: git remote 指向本仓自身时 push 返回 up-to-date 是假象——产出未离机。核验远端必须 ls-remote 看真实 URL 与 ref，不能只看 push 输出。
first_seen: 2026-09-26
last_verified: 2026-09-26
skills: [事实核验, 验证与交接]
evidence_refs:
  - commit: fc9521f9   # LOG 清场补扫段（origin 自环发现留痕）
  - commit: 602d315a   # logs/ 撤跟踪 + github remote 重接后 master
  - decision: D-220    # 产出即提交条款——815a7e6 无备份实证即本坑前置先例
---

## 现象

`git push origin master` 返回 `Everything up-to-date`，看似已同步远端；实际 `git remote -v` 显示 `origin = /opt/arb`——remote 指向本仓自身。push 只是在同一 refs 空间里更新镜像，`refs/remotes/origin/*` 因此永远等于本地 head，核验命令（`git rev-parse HEAD origin/master`）也永远一致——**自证循环，无任何离机信号**。

## 根因或策略解释

- 多席位 worktree 架构下，各席「push 到远端」=推到部署树自身 ref 域——自环是有意机制（席位分支共享），但 master 的「已推送」判断同样被它短路。
- 迁移/历史重写类操作（9-13 迁机、9-14 filter-repo）是 remote 配置被改写/丢失的高发窗口——配置不入 git 版本，丢无告警。
- 伴随效应：GitHub 真实仓库静默掉队（实证 9-22→9-26 四天 50+ commit 零离机），磁盘级故障=全损。

## 处置

1. 怀疑即查 `git remote -v`——URL 是本机路径/本仓路径=自环，不是远端。
2. 核验用 `git ls-remote <真实URL> refs/heads/master` 比对 hash——不依赖 push 输出与本地镜像 ref。
3. 修法=**加具名第二 remote**（如 `github`）推 master，保留 origin 自环不动（席位共享机制依赖它）。
4. 清场/迁移/历史重写后例行核验 remote 拓扑；备份是责任不是默认。

## 证据

- check: `git remote -v` → `origin /opt/arb`（fetch+push 同址）
- check: `gh repo list` → `myfxlogs/arb` private 存在，last push 9-22
- check: `git push github master` → `46437990..fc9521f9` 成功，四天产出离机
- result: origin 自环保留（席位机制）+ github remote 重接，master 双写恢复
- ref: commit `602d315a`（logs/ 撤跟踪）、LOG「2026-09-26 工位清场·补扫」段

## 反证条件

- check: `git ls-remote` 两个 remote 各自返回独立 ref 状态
- falsifies_when: 若 github remote 配置遗失或 URL 失效（凭据轮换/仓库改名），ls-remote 报错即暴露——需 periodic 核验而非一次性假设
