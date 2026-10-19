---
schema_version: 2
id: worktree-node-modules-self-loop
kind: pitfall
risk: medium
status: verified
keywords: [worktree, node_modules, symlink, 自指, 环链, npm ci, web 构建, 前端]
scope: [worktree, web, 前端构建, 环境基建]
summary: worktree 的 web/node_modules symlink 指向主树同路径=自指环链（ln 时目标已是 symlink 原样再链）——全 worktree npm 面断裂；修法=rm 断链+主树 npm ci+build，防=禁对已 symlink 路径再 ln
first_seen: 2026-10-18
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: server_fact
    ref: /opt/nox/web/node_modules -> 自身（symlink 环链实拍，10-18）
  - type: doc
    ref: docs/handoff/STATE.md D-623 收口后续注记行（待归纳 10-18 决策席行）
repro_or_symptom: worktree 内 npm install/build 全挂（ENOENT/自指路径）；`ls -l web/node_modules` 显示指向自身或环链——根因是各 worktree 的 web/node_modules 约定为「wt→主树」symlink，某次在主树路径上再 ln -s（目标=已存在的 symlink 本身）成自指
root_cause: symlink 目标解析不做于 ln 时——`ln -s a a` 类操作把环链固化；约定（wt→主树）缺机械守卫
fix_and_prevention: ①修复=rm 环链+主树 `npm ci`+`npm run build`（wt 链自愈）②防=对 node_modules 类约定 symlink 路径操作前先 `readlink` 验非环；禁在 symlink 已存在路径上再 ln ③新 worktree 建立时检查 wt/web/node_modules 是否正确指向主树
notes: 与 [[worktree-web-dist-embed-gate]] 同族（worktree 生成物断链族）但根因不同：本条=符号环链，彼条=gitignored 生成物缺席
---

## 现象

全 worktree 的 npm 面（install/build）断裂；`web/node_modules` symlink 指向自身成环。

## 根因或策略解释

worktree 约定 web/node_modules=「wt→主树」symlink；在某路径已存在 symlink 时再执行 ln，目标解析延后→自指环链固化。

## 处置

rm 环链+主树 `npm ci`+`npm run build`，wt 链自愈（D-623 收口后续实修）。

## 证据

- repro: `ls -l /opt/nox/web/node_modules` 环链实拍（10-18）
- result: 修复后各 wt npm 面恢复

## 反证条件

- check: worktree node_modules 链型改为实体复制或包管理器 workspaces 原生支持
- falsifies_when: 链型约定被替换，本条降级为历史教训

## 适用边界

本仓及一切采用「wt→主树」symlink 约定的多 worktree 仓。
