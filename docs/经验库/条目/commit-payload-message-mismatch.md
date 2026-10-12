---
schema_version: 2
id: commit-payload-message-mismatch
kind: pitfall
risk: high
status: verified
keywords: [commit, 提交信息, 载荷, git show --stat, 收编, git add 遗漏, 多树并发, reset --soft, 重建 commit]
scope: [git 工作流, 多 agent 多树协作, 收编纪律]
summary: 提交信息描述的改动不在 commit 里（git add 遗漏载荷）——收编/信任前必核 git show --stat 与信息一致；不一致时从工作树字节对账定权威版本再 reset 重建
first_seen: 2026-09-07
last_verified: 2026-09-07
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: 82fa1f2
  - type: doc
    ref: docs/D-234批1复审-2026-09-06-Claude.md
---

## 事实/症状

D-234 批 1 返修交付：commit cc09f36 信息宣称 R-F1/R-F2 测试返修完工（含先红证据描述），
`git show --stat cc09f36` 实际**仅 docs/handoff/STATE.md 1 行**（LOG 节指针修正）。
真实载荷（2 个 _test.go）以未提交修改散落在部署树 /opt/arb 工作树；同时 seat/zcode 树还
staged 了另一份**分叉变体**（同修 R-F1 但违规删除 TestD245_ReconcileLeggedTriggerLogged 治理测试）。
若收编方只看提交信息（或只看 staged 变体）即收编，master 将（a）不含宣称的修复 或（b）丢治理测试。

## 根因或策略解释

施工方跨树作业（部署树+seat 树并行改动）+ `git commit` 时 add 遗漏载荷文件；
提交信息按「本次会话完成了什么」撰写而非按 staged diff 撰写 → 信息与内容静默脱钩。
多树并存时同一返修各自演化，产生语义分叉（一个最小合规、一个删测试省事）。

## 处置

收编方四核（9-7 补偿事故后升格为「三核+四步」）：
0. **核基底=被复审头**：收编对象 commit 的 parent 链必须含复审通过的那个头。9-7 事故：返修 commit
   直接落在旧基底 6cb756b 上而非被复审头 17fd4dd 之上，收编只核了载荷与信息一致，整条批 1 链
   静默丢弃且 force-push 后无远端可达——「CI 绿」会掩盖缺基底（被跳过的恰是 CI 不跑的面）。
1. `git show --stat <commit>` 对照提交信息逐项核验载荷——不一致即整包不信任
2. 字节对账各树副本（`git show :file | diff - 目标树file`），以「与复审指令形态匹配+含全部治理测试」定权威版本
3. commit 未推送 → `git reset --soft <基底>` 后显式 add 载荷文件重建 commit（信息补交付缺陷留痕）；
   已推送 → 禁改史，前向修复 commit
4. 违规变体书面拒收+留痕，通知该 seat reset 对齐新 master（防其后续 commit 复活删测试版）

验证：重建后负探针/机检/CI 全绿（82fa1f2 → CI success，修复前 6cb756b=failure 对照）。

## 证据

- check: `git show --stat cc09f36`
- result: 仅 STATE.md 1 行，与提交信息宣称的 R-F1/R-F2 载荷不符
- check: `git show :internal/reconcile/close_governance_test.go | diff - /opt/arb/.../close_governance_test.go`（seat/zcode staged vs 部署树）
- result: DIFFER——staged 变体多删 30+ 行（整个治理测试+import）
- ref: commit 82fa1f2（重建+留痕）

## 反证条件

- check: 对任一 commit 常规执行 show --stat 对照
- falsifies_when: 长期零失配 → 说明单树单会话纪律下该核验冗余（多树/跨席场景仍建议保留）

## 适用边界

- 适用于收编（ff/merge/cherry-pick 他人工作）与信任任何非亲历 commit 前
- reset --soft 重建仅限未推送 commit；已推送改走 revert
- 显式 add 文件清单，禁 `git add -A`（会扫入同树他人未提交改动）
