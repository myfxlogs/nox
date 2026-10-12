---
schema_version: 2
id: bash-cwd-persists-across-calls
kind: pitfall
risk: medium
status: verified
keywords: [bash, cwd, 工作目录, 残留, cd, git -C, 绝对路径, agent 工具链]
scope: [agent bash 工具调用, 任何跨调用 shell 环境, 多仓操作]
summary: agent 的 bash 调用之间 cwd 持久残留——上次 cd 进子目录后，下次相对路径命令静默失败或写错仓（git 推错仓实证）；仓库命令一律绝对路径/显式 cd 开头，git 用 -C。
first_seen: 2026-08-24
last_verified: 2026-08-28
skills: [验证与交接]
evidence_refs:
  - type: decision
    ref: D-130
  - type: doc
    ref: scripts/hooks/pre-commit
---

## 事实/症状

会话中先执行 `cd tools/arbkb && go run . gen`，之后执行 `git add docs/...` → FileNotFoundError 或 add 到错误路径；另一实证：未用 `-C` 的 git 命令在错误仓执行并推送。

## 根因或策略解释

agent 的 bash 工具是长驻 shell，cwd 跨调用保留（不像人类一键一新终端）；相对路径的语义随上次调用漂移，失败模式静默（写错位置）而非显式报错。

## 处置

三条防线：①仓库命令一律绝对路径或行首显式 `cd /abs/path &&`；②`git` 一律 `git -C <repo>`；③对「 inexplicable 的文件找不到」先 `pwd` 自检。

## 证据

- check: 2026-08-28 D-127 收工会话两次复现（arbkb 子目录残留致 python 脚本 FileNotFoundError）
- ref: 私有记忆 bash-cwd-persists-trap（2026-08-24 首次实证：一次致错一次推错仓）

## 反证条件

- check: 若 agent 工具链改为每次调用独立 shell
- falsifies_when: cwd 不再跨调用残留（本条退役）

## 适用边界

通用 agent 工具链坑（Claude/Devin 同构）；人类一键一终端场景不适用。
