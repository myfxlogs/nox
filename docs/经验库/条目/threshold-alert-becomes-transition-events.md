---
schema_version: 2
id: threshold-alert-becomes-transition-events
kind: strategy
risk: medium
status: verified
keywords: [告警, 阈值滤, 事件式, transition, 每日重报, 稳态零报, 狼来了, 冷却, flap 抑制, new_entry, sign_flip, exit_band]
scope: [监控告警设计, 轮询型扫描器, 通知管道]
summary: 阈值型告警（条件成立即报）对持续态信号必然逐轮重报（狼来了）——阈值再紧也只降量不治重复；正解=改事件语义只在状态转移时发声（new_entry/sign_flip/exit_band 三类 transition+稳态零报），「行从快照里消失」本身不是 exit_band（另有消失类事件的防双报归属），原 24h 冷却降级为 flap 抑制（进出带抖动至多一报/日）
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: 481a33c8
  - type: doc
    ref: tools/universe-radar/diff.go fundingTransitions
---

## 事实/症状

radar T3 funding 告警为纯阈值滤（|ann|≥15% ∧ vol≥$5M 即报），符号持续在档即每日重报（实证 26 条/日）——接收方脱敏（狼来了）。把阈值调紧/提高只降条数，重复语义不变。

## 根因或策略解释

阈值滤回答「现在糟不糟」，告警读者要听「发生了什么变化」。持续态（极端 funding 持续数周）在阈值语义下每轮都是「新息」，在事件语义下只有第一次值得发声。

## 处置

改事件语义：对每行比对上轮快照（old map，零新 IO），只在转移时发——new_entry（新进带）/sign_flip（符号翻转）/exit_band（跌出带，行仍在）；稳态零报。两个防双报/防漏报细节：①行整体消失≠exit_band（另有 DELISTED 类消失事件，双归属即双报）；②旧 24h 冷却保留但语义降级为 flap 抑制（在带缘抖动至多一报/日）。文案带 kind 标签+旧→新两侧数值。

## 证据

- check: tools/universe-radar 生产实证 26 条/日重复推送；改造后 T1 稳态锚红→事件式零报（commit 481a33c8 变异轮：摘除稳态抑制→STEADY/NEGRIGHT 精准红）
- ref: seat/zcode-d353 @481a33c8

## 反证条件

- check: 若某信号类不存在「转移」语义（纯瞬时脉冲，如每轮独立的成交回报），事件化反而不适用
- falsifies_when: 事件式改造后接收方仍报持续态重复（转移判定漏稳态分支）

## 适用边界

- 轮询型扫描器/快照 diff 架构（有 old 可比）；无事件流上游同理适用（本条即 stream-over-snapshot 不适用场景的正解）；秒级实时告警不在此列。
