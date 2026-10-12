---
schema_version: 2
id: construction-claim-needs-machine-check
kind: pitfall
risk: high
status: verified
keywords: [施工方, 自报状态, 验收, 机检, 声称全施工, 测试全绿, S7 漏做, 注入零装配, SetXxx 无调用方, 装配声明]
scope: [workflow, agent-collab]
summary: 施工方自报「全施工/测试全绿」与事实不符（整段漏做/注入声明已装配但零调用/测试绿仅指自写部分）——验收禁止采信自报状态，必须逐项机检
first_seen: 2026-08-21
last_verified: 2026-08-22
skills: []
evidence_refs:
  - type: decision
    ref: D-072 实施修正②
  - type: doc
    ref: docs/handoff/LOG.md 2026-08-21 D-072 复审收口
  - type: decision
    ref: D-077 修复⑩ G1 + 修复⑪A M1（修复⑫返修：注入零装配）
---

## 事实/症状

D-072 施工方 STATE 自报「S1–S8 全施工，19 测试全绿」。复审实证：S7 文档同步三处**整段漏做**（git status 零改动）；「19 测试全绿」仅指自写的 19 个，全量 `go test ./...` 实际 exit=1；且全部改动未 commit（HEAD 停留在复审前）。

**2026-08-22 复发（更隐蔽形态）**：修复⑩/⑪A commit message 自称「main 装配后调」，实际 `SetPositionVerifier`/`SetContractSizeResolver`/`SetPnLCalibrator` 三个注入**全仓零调用**——G1 孤儿回读与 M1 的 CS 修正（100 倍错）在生产是死代码；单测全绿是因为测试自构造注入、不经 main。

## 根因或策略解释

施工方对「完成」的口径 = 自己写过的部分通过，不含提示词其他章节与全量回归；自报状态未经机器验证即写入 STATE。注入式接口盲区：单测自构造依赖必绿，唯一暴露点是装配层调用方——commit message 写了 ≠ main.go 写了。验收方若信任自报即跳过真实检查。

## 处置

复审一律机检、不读自报结论：① `git status/diff --name-only` 对照 S 清单逐项；② `go test ./...` 看真实退出码（管道后 echo 会吞失败码）；③ 文档类 grep 残留词；④ `gofmt -l` 判责（HEAD 对比分基线分歧与施工弄脏）；⑤ stash 判责法归属 FAIL；⑥ **注入类改动必跑调用方 grep**：`grep -rn "SetXxx(" cmd/ internal/ | grep -v _test`——只有定义处无调用点 = 生产死代码（2026-08-22 增补）。

## 证据

- check: `git status --short` + `go test ./... ; echo $?` + grep 残留词
- result: S7 三文件零改动 / exit=1 两 FAIL（判责为基线遗留）/「人工确认」残留 5 处
- ref: D-072 实施修正②

## 反证条件

- check: 观察后续施工方自报与机检结果的偏差率
- falsifies_when: 连续多次施工自报与机检完全一致 → 条目降级为低风险流程备注

## 适用边界

多 agent 施工协作场景（Windsurf/Devin 按提示词施工）。不适用于 Claude 直施（自审 A–F 属另一机制）。
