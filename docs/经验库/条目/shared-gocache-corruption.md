---
schema_version: 2
id: shared-gocache-corruption
kind: pitfall
risk: low
status: verified
keywords: [GOCACHE, 并发 agent, package unsafe is not in std, setup failed, 假失败, 隔离缓存]
scope: [多 agent 同机, go 工具链]
summary: 多 agent 同机并发跑 go 命令共享 GOCACHE 会互相踩坏缓存——症状为 setup failed "package unsafe/internal/reflectlite is not in std" 等标准库失踪假错误（build 可能过而 test 崩）；隔离 GOCACHE（每会话独立 /tmp/gocache-*）即愈，代码无缺陷。
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: d762f44
---
## 事实/症状
两 agent 并发（ZCode 复审+Devin 施工同机），go test 报 `package internal/reflectlite is not in std`/`package unsafe is not in std`（setup failed），同 worktree go build 却 exit=0。
## 根因
共享 ~/.cache/go-build 被并发写坏；错误指向标准库为误导。
## 修法
复审/长会话 go 命令带 `GOCACHE=/tmp/gocache-<用途>` 隔离；遇到"标准库 not in std"先怀疑缓存而非代码。
## 反证条件 / 适用边界
单 agent 场景不适用；若隔离后仍复现才是真环境问题（GOROOT 损坏）。
## 泛化判断
可晋升 ~/kb（多 agent 同机开发通用）。随下次收工晋升。
