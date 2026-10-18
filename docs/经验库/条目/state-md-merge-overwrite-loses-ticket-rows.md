---
schema_version: 2
id: state-md-merge-overwrite-loses-ticket-rows
kind: pitfall
risk: high
status: observed
keywords: [STATE.md, 合并冲突, 丢票行, merge conflict, ticket row loss, 票据唯一真相源, 多席并发]
scope: [docs/handoff/STATE.md 多席并发写冲突解决, 任何「每行一票」式单文件账本的人工/半人工合并]
summary: STATE.md 票行在多席并发提交合并解冲突时被静默整行覆盖丢失（f1763dae7 解冲突丢 28dc77d4e 两票行实证）——票据是派单唯一真相源，丢行=票蒸发且无任何报错信号。修法=解合并后 grep -c '^| \*\*' 前后对拍+冲突段逐行保新票行；已丢则按行史 git show <旧commit>:STATE 补回。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: commit
    ref: 28dc77d4e（两票行立项：user-ws MarkFrameOK+CMC instrument 订阅取证）
  - type: commit
    ref: f1763dae7（合并覆盖丢行——bn-capability 收口#73 STATE 提交基于旧版文件）
  - type: commit
    ref: d29224fb8（两行补回修复）
  - type: file
    ref: docs/handoff/STATE.md（票表段——每行一票唯一真相源）
---

## 事实/症状

STATE.md 票表段每行一票是派单唯一真相源（§4 工作流）。多席并发提交同文件时合并冲突解「取旧版整段覆盖」会把对手席在窗口期内新立的票行整行抹掉——无声无息，git 层面无报错，票凭空蒸发（下游领票/复审全找不到行）。实证：f1763dae7（bn-capability 收口#73 STATE 提交）解冲突时基于旧版文件覆盖，丢 28dc77d4e 立的两票行（binance user-ws MarkFrameOK 接线+CMC instrument 价频订阅取证）；实施席 grep 票行失败后才发现丢行，按 `git show 28dc77d4e:docs/handoff/STATE.md` 行史补回（d29224fb8）。

## 判定/复现

- 任何 STATE.md 提交后 `grep -c '^| \*\*'` 行数对拍——行数比基线少=丢行红灯。
- 解合并态 `git diff` 冲突段逐行核对 `| **` 前缀票行：新立行、翻态行都须保留；只留本侧改动行（旧态行被侧方更新过的不能回滚）。
- 丢行恢复：`git log --follow --oneline -- docs/handoff/STATE.md` 找立项 commit → `git show <c>:docs/handoff/STATE.md | grep <票名>` 取原行 → 按现行态翻态后补插票表段。

## 规避/修复

- STATE 提交前自查行数+本票行在册；合并冲突段解后跑 `grep -c '^| \*\*'` 前后两版对拍。
- 单文件账本多席写是结构病灶根源——长期件=票表迁行级 append-only 件（一行一文件）或锁文件机制，当前先记不立项。
