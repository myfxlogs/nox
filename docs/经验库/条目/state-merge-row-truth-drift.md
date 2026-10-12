---
schema_version: 2
id: state-merge-row-truth-drift
kind: pitfall
risk: medium
status: verified
keywords: [STATE.md, merge, 行真值回退, --no-ff 收编, 冲突解决, grep 复核, stale row, 施工表]
scope: [交接协议, 收编流程, 多席位协作, STATE.md 维护]
summary: seat 分支 --no-ff 收编时 STATE.md 施工表行会带回分支基线的旧态行（或冲突解决覆盖终态行）——merge 后必须 grep 复核每个当日 D# 的行真值，否则终态回退成「报审/已转发」旧态静默漂移
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: [验证与交接]
evidence_refs:
  - type: doc
    ref: docs/handoff/LOG.md「D-354 复审+收编部署」段（D-352 行回退实例+修正 5fa1fad）
  - type: commit
    ref: 5fa1fad3
  - type: doc
    ref: docs/handoff/LOG.md 2026-09-19 凌晨 D-337 段（首证实例：两次 silent-replace no-op）
---

## 事实/症状

--no-ff merge 收编 seat 分支后，STATE.md 出现行真值漂移两类：①分支基线早于 master 终态→合并把旧态行（🔄 施工完成报审/已转发）与 master 终态行（✅）并存或以冲突形态呈现，解决时只处理冲突块、漏改非冲突区的旧态残留；②python/Edit 部分替换 no-op（命中失败静默）。9-19 两例：D-352 ✅ 行在后续合并中回退 🔄 旧态（当晚 D-354 收编时 grep 复核抓回）；D-337 时段两次 silent no-op 靠后验才发现。

## 根因或策略解释

STATE.md 是多席共写热点文件，seat 分支基线几乎必然落后 master 终态；git 三方合并按文本块工作，不识别「行语义=任务状态」——旧态行与终态行在同一文件不同位置时合并零冲突静默并存。施工表行真值=项目交接 SSOT，回退一行=下个会话读错状态。

## 处置

**merge 收编落 commit 前固定三步**：①`grep -n '<<<<<<<\|>>>>>>>\|^======='` 冲突标记清零；②`grep "D-<当日收编号>" STATE.md` 逐行核对状态列（🔄 报审/已转发残留→并成单行 ✅ 终态，含 merge hash）；③`wc -c` 预算复核。部分替换（python/Edit）必须 assert 命中（no-op 静默=假完成）。

## 证据

- check: D-354 收编夜 `git show 269e22ec:STATE.md` 与工作树 diff——D-352 旧态行漂移实证，修正=5fa1fad
- check: D-337 夜 LOG 记载「两次 silent-replace no-op 靠业主/后验才抓」
- result: 三步复核当夜抓回漂移行，零流失
- ref: D-337 / D-352 / D-354 收编流程

## 反证条件

- check: 若 STATE.md 改为单席位独写（或行级原子合并机制）后连续 10 次收编零漂移
- falsifies_when: 漂移不再可能发生=本条降级为历史记录

## 适用边界

- 适用：一切 seat 分支 --no-ff/--ff 收编、STATE.md 冲突解决、python/Edit 程序化改 STATE。
- 不适用：非 STATE 的纯文档 append（无行语义状态）。
