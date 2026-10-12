---
schema_version: 2
id: merge-commit-signature-gate-blocked
kind: pitfall
risk: medium
status: verified
keywords: [收编, merge commit, 署名门禁, pre-commit, 署名 Claude, ff 收编, rebase 席位分支, pre-push 白名单]
scope: [git 工作流, 收编流程, 多席并发, pre-commit hook]
summary: 非 Claude 席 worktree 里做 merge commit 收编会被署名门禁 §7 拦（seat 分支带入的 Claude 亲签段落=staged 新增行）——正解走 practices 既定「rebase+ff 收编」：席位分支 rebase 到 master 前沿（rebase 不跑 pre-commit）→部署树 git merge --ff-only→push master（白名单），全程零新 commit 零门禁触发。
first_seen: 2026-09-16
last_verified: 2026-09-16
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: commit
    ref: d6328882
  - type: commit
    ref: 64845c54
  - type: doc
    ref: scripts/hooks/pre-commit:76-98
  - type: doc
    ref: docs/handoff/practices.md（多席并发纪律 §5 收编纪律）
---

## 事实/症状

Devin 施工席在部署树 `/opt/arb` 执行 `git merge --no-ff origin/seat/binance-fix` 收编 D-300 设计文档族，pre-commit §7b 报 FAIL：

```
FAIL: 新增署名行含 Claude，但本会话非 Claude 席——署名到人，Devin 署名用 Devin：
+> <席位署名行两行，内容为 Claude 席签名，此处不写原文以免门禁正则自命中>
```

merge commit 的 staged diff = HEAD→合并结果全量差分，seat 分支带入的新文档里 Claude 亲签的终裁段被判定为「本会话新增署名 Claude」。门禁按 worktree 路径判席（`/opt/arb-wt-claude*` 豁免），部署树/其他席位树一律拦截。D-221 禁改他人署名，无法靠改文档过门。

## 根因或策略解释

门禁 §7 的本意是拦「Devin 冒名签署 Claude 文档」（D-256），但未区分「创作署名行」与「merge 带入既有署名行」——对收编场景是误报。绕法（exempt 路径提交/改门禁）都不该用。

## 处置

走 practices.md「多席并发纪律」第 5 条既定收编流，全程不产生新 commit：

1. 席位 worktree 内 `git rebase master`（rebase 不触发 pre-commit；本例仅 STATE.md 冲突，取 master 新版——seat 侧为已滚出旧行，replay 后该 commit 自然变空被丢弃）
2. `git push --force-with-lease origin seat/<分支>`（重写后同步远端）
3. 部署树 `/opt/arb`：`git merge --ff-only origin/seat/<分支>`（ff 不产生 commit）→ `git push origin master`（/opt/arb 在 pre-push 白名单）

实证：D-300 收编 seat/binance-fix → master d6328882（设计 v3.0 replay 为 718a7be6，STATE 引用 hash 需同步更新）。

## 证据

- check: `git merge --no-ff origin/seat/binance-fix` + commit in /opt/arb → pre-commit FAIL（署名 Claude 行）
- result: 改走 rebase+ff 后收编成功，master d6328882
- ref: commit 64845c54（S0 留档）/ LOG 会话纪要 2026-09-16

## 反证条件

- check: 在席位 worktree 对含 Claude 署名段的文档族做 merge commit
- falsifies_when: pre-commit §7b 对 merge commit 增加豁免或按「行作者 vs 提交者」区分后不再拦

## 适用边界

- 仅适用于「收编带进他人署名文档」场景；本席位自创文档署名仍按席位名签署。
- rebase 会改写 seat 分支 commit hash——STATE/提示词中引用的旧 hash（如 @a7bd7def）需在收编记录中更新为新 hash。
