---
schema_version: 2
id: merge-residue-breaks-master-build
kind: pitfall
risk: high
status: verified
keywords: [merge 冲突, 残件, master build 破, import 丢, 机检独立重跑, 产物级验证]
scope: [review, merge, build, ci]
summary: merge 冲突解残件可让 master build 直破——denominator-live merge 落了 dciexec.SetLiveEquityMapFn 调用但 import 行丢，`go build` 实拍 undefined；「merge 后未跑 build」=复审产物级验证失守
first_seen: 2026-10-18
last_verified: 2026-10-18
skills: []
evidence_refs:
  - type: commit
    ref: a05aa8972（破）→ 3c006a81e（修：补 import 行）
  - type: server_fact
    ref: "master @a05aa8972 `go build ./cmd/core` → `undefined: dciexec`"
repro_or_symptom: merge 解冲突只保语义行不验证编译——调用点留了、import 丢了；分支各自绿而 merge 产物红
root_cause: 复审机检独立重跑只跑了分支 build，未对 **merge commit 后的 master** 实跑 build——四件套②产物级验证漏了 merge 产物本身
fix_and_prevention: ①merge commit 落 master 后**必跑 `go build ./...`**（或等效最小面）再推/再收——分支绿≠merge 绿②工作区有未提交修件时 merge 前先盘 `git status` ③收口四件套②的产物级验证含「merge 产物 build 实测」非仅分支 build
notes: 同类失守同族：方案核验/机检重跑只验证「分支态」不验证「merge 后态」
---

## 现象

`denominator-live` merge（a05aa8972）把 `dciexec.SetLiveEquityMapFn` 调用落进 main_monitors.go 但 import 块丢了 `nox/internal/trading/dciexec`——master 上 `go build` undefined 直破；而工作区躺着的未提交补行无人收尾。

## 根因或策略解释

冲突解只比对了语义行（调用点在两分支各自上下文都合法），import 行是「编译期胶水」不被冲突提示关照；merge 产出态无人再跑 build——复审机检验的是分支态。

## 处置

补 import（@3c006a81e）+build 验绿。规约化：merge commit 落 master 后必跑 build 再推；复审产物级验证对「merge 后 master」实测非仅对分支。

## 证据

- check: master a05aa8972 `go build ./cmd/core`
- result: `undefined: dciexec`；修复后绿

## 反证条件

- falsifies_when: CI 若在 merge 自动跑 build（门禁侧覆盖），手工重跑可省——当前无 CI build 闸，手工必检

## 适用边界

一切 merge 到 master 的 commit；多分支并日 merge 时为最高发场景。
