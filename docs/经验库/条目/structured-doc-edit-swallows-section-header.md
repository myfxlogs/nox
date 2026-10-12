---
schema_version: 2
id: structured-doc-edit-swallows-section-header
kind: pitfall
risk: medium
status: verified
keywords: [Edit替换, 插段, 段头吞失, 结构化文档, decisions.md, 孤儿bullets, 门禁盲区, 多agent并发]
scope: [handoff 文档, decisions.md, 多 agent 协作]
summary: 以「## 段头」为锚用字符串替换在结构化文档插入新段时，锚点行被消费——new_string 忘记回接原段头则该段成孤儿 bullets，且 pre-commit 门禁（行数/关键词/预算类检查）不拦截结构删除；多 agent 并发编辑下更难即时发现，破坏无损交接 SSOT。
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: [KB维护, 事实核验]
evidence_refs:
  - type: commit
    ref: 9e3ea00
  - type: commit
    ref: fb2c343
  - type: decision
    ref: D-217
---

## 事实/症状

2026-09-04 在 decisions.md 以「## D-213/214/215 审计…」段头为 old_string 插入 D-217 新段，new_string 只含新段内容未回接段头 → 该审计段 4 条 bullets 失去标题成孤儿；commit 9e3ea00 通过 pre-commit 门禁放行，同日 fb2c343 自查发现并修复（非门禁发现）。

## 根因

字符串替换编辑是「替换」不是「插入」：锚点本身被消费。diff 自审注意力在新内容，未核对锚点行是否保留；门禁检查（行数预算/关键词/TODO）均不感知 markdown 结构完整性。

## 修法

①以段头为锚插段时，new_string 尾部必须原样回接该段头（模板=新段全文+空行+原段头原文）；②提交前 `grep -c '^## '` 只增不减专项核对；③优先锚定目标位置前一行的末尾内容，不动段头。ZCode 席位私有记忆另有通道条目（section-edit-guard）。

## 反证条件 / 适用边界

仅适用于「以段头为 old_string」的替换式编辑；行内 bullet 追加式插入不受影响。若未来 pre-commit 新增 markdown 结构完整性检查（段落数只增不减），检测兜底失效但行为修法①③仍必要。

## 泛化判断

可晋升 ~/kb（任何多 agent 结构化 markdown 仓同构：Edit 工具语义在主流 agent CLI 一致）——随下次收工晋升流程走。
