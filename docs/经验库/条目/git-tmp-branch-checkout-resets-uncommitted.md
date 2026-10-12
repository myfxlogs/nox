---
schema_version: 2
id: git-tmp-branch-checkout-resets-uncommitted
kind: pitfall
risk: high
status: verified
keywords: [git, tmp 分支, checkout --, 未提交改动丢失, 分支同步, 工作区重置, master 同步]
scope: [任何用临时分支做正向同步的工作流, 多席位多分支仓库]
summary: "用临时分支同步内容（`git checkout -b tmp origin/master && git checkout <src> -- 文件 && commit && push`）时，若工作区存在**已修改未提交**的同名文件，往返 checkout 会把它们静默覆盖丢失——一天内同坑三次（代码重写三次）。根因：`git checkout <ref> -- <path>` 无条件用目标版本覆盖工作区+index，不检查未提交改动；且 commit 被门禁 FAIL 拦下时 index 里的 add 不回滚，后续 checkout 照样覆盖。修法：起 tmp 分支前强制 `git status --short` 确认工作区干净（或先 commit/stash）；门禁 FAIL 后立即 `git restore --staged` 清 index 再走 tmp 流程；重要改动先 commit 到自己的分支再同步（commit 是最好的备份）。"
first_seen: 2026-09-09
last_verified: 2026-09-17
skills: [验证与交接]

---

## 变体（2026-09-17 Devin 补录）

变异测试还原场景同族：D-318 施工 T1 变异还原用 `git checkout -- <file>` 撤销刻意变异，连带洗掉同文件未提交的 S1 新码（~30 行，原样重贴+全绿复验闭环）。机制同一——checkout 对未提交改动无差别覆盖。处置面扩展：**变异还原先 `cp` 备份再改（或 stash）**；本坑不限于 tmp 分支同步流。
