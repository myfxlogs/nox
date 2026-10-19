---
schema_version: 2
id: worktree-web-dist-embed-gate
kind: pitfall
risk: medium
status: verified
keywords: [worktree, web/dist, embed, go:embed, gitignored, 生成物, go build, buf generate]
scope: [worktree, go build, web, embed]
summary: 新 worktree 缺 web/dist（gitignored 生成物）→ go build ./... 于 embed 门禁即炸（embed 报 pattern all:dist 缺匹配文件）；处置=主树 cp -r web/dist 到 wt，零前端触面时内容无关
first_seen: 2026-10-18
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: doc
    ref: web/embed.go:10（go:embed all:dist）
  - type: server_fact
    ref: 10-19 pipeline-watch worktree 实测——build 报 pattern all:dist 缺匹配文件，cp 后过
repro_or_symptom: 新 worktree `go build ./...` 报 embed.go 第10行 pattern all:dist 缺匹配文件——web/dist 为 gitignored 构建产物，worktree 检出不携带；§6 既有注记只提 buf generate（web/src/gen）侧，dist 侧同理
root_cause: go:embed 要求目录在编译期实存；gitignored 生成物不经 git 检出传播，新 worktree 天然缺
fix_and_prevention: ①主树 `cp -r /opt/nox/web/dist <wt>/web/dist`（零前端触面票=内容无关语义，任何在产 dist 可用；涉前端票须 wt 内 npm run build 自产）②§6 机检清单的 worktree 前置注记应并提 buf generate 与 dist 两件
notes: 与 [[worktree-node-modules-self-loop]] 同族（worktree 生成物断链族）但根因不同：本条=gitignored 缺席非环链
---

## 现象

新 worktree `go build ./...` 在 web 包 embed 门禁炸（dist 不存在），与代码无关。

## 根因或策略解释

go:embed 编译期硬性要求匹配文件实存；dist 为 gitignored 生成物，worktree 检出不含。

## 处置

主树拷贝 dist 到 wt 即过（零前端触面票）；涉前端票在 wt 内自建。10-19 pipeline-watch 票实测通过。

## 证据

- repro: pipeline-watch worktree build 失败→cp 后过（10-19）
- ref: §6 已有 buf generate 同族注记（web/src/gen 侧）

## 反证条件

- check: dist 纳入 git 或 CI 产物缓存下发 worktree
- falsifies_when: 生成物流转机制变更后本条降级

## 适用边界

一切带 go:embed 生成物、多 worktree 并行的仓。
