---
schema_version: 2
id: conflict-marker-keyword-collision
kind: pitfall
risk: medium
status: verified
keywords: [git rebase, 冲突解决, python 脚本, marker 行, 关键词匹配, STATE.md, 收编]
scope: [git 冲突解脚本, STATE.md 表格行, 多席并发收编]
summary: 脚本解冲突按关键词选行时，>>>>>>> marker 行本身含提交信息里的关键词（如 "D-406"）会被误选为内容行——真行丢弃、marker 变正文进 master。解冲突脚本必须先剔除 <<<</====/>>>> 三 marker 行再匹配。
first_seen: 2026-09-23
last_verified: 2026-09-23
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: 694d2466
  - type: commit
    ref: 307acb7e
---

## 事实/症状

D-405/D-406 栈式收编时 rebase STATE.md 冲突，脚本解冲突逻辑：
`d406_theirs=[l for l in s[j:end].split('\n') if 'D-406' in l][0]`——
`s[j:end]` 恰是 marker 行 `>>>>>>> ddaeb16d ([角色:施工] D-406 S1-S5...)` 本身，
提交信息含 "D-406" 字样被误选。结果：marker 变正文行进 master、真 D-406 行丢失。

## 根因或策略解释

冲突 marker `>>>>>>> <hash> (<commit message>)` 的括号内是完整提交信息，
提交信息常含任务编号（D-4XX）——按任务号选行的逻辑必然命中 marker。
同理 `<<<<<<< HEAD` 和 `=======` 行虽不含关键词，但选行范围若没先切分
三段（HEAD/theirs/marker），边界 find 偏移一个字符就会把 marker 括进内容。

## 处置

- 解冲突脚本铁律：先按 `^<{7}|^={7}|^>{7}` 剔除全部 marker 行，再在净文本里按关键词选行。
- 或者更稳：用 `git checkout --theirs/--ours <file>` 取整侧再人工编行级合并，别写逐行脚本。
- 提交前 `grep -n '<<<<<<<\|=======\|>>>>>>>'` 兜底检查（本次事后 grep 一眼可见残留）。
- pre-commit 对 marker 残留无门禁——这次 marker 行进 master 实证此检查值得补（可候选加门禁）。

## 证据

- check: `grep -n '>>>>>>>' docs/handoff/STATE.md`（收编 commit 694d2466 后）
- result: line 52 残留 `>>>>>>> ddaeb16d (...)` 正文行，D-406 表格行整行缺失
- ref: 修复 commit 307acb7e（真行补回+14 行滚动）

## 反证条件

- check: 若 marker 行不含任何任务关键词（如纯 hash 无 message），关键词选行不会误命中
- falsifies_when: marker 行不含关键词的场景下该坑不触发——但提交信息惯例必带 D# 编号，故在本仓规约下恒触发
