---
schema_version: 2
id: multi-agent-shared-tree-residue-recovery
kind: pitfall
risk: medium
status: observed
keywords: [多 agent, 共享工作树, 未提交残留, stash 冲突, 平行基底, 脏合并, cherry-pick 收编, 字节等价核验, 部署旧代码]
---
# 多 agent 共享树的残留/冲突三层污染与无损恢复序列

## 症状（三层污染叠加）
①施工 agent cwd 重置后把编辑落错树（共享部署树出现他人未提交的 proto/gen 残留）；②处理 digest stash 时 pop 撞冲突阻断 pull；③阻断未被察觉时 build+deploy 照跑——**部署出旧 HEAD 的二进制**（复合命令里 `;` 与 `&&` 混用+pull 失败不中断链）。

## 根因
多席位共享一个工作树+一个 index，任何一席的 add/stash/checkout 作用于全体（D-243 立项根因）；恢复动作与构建部署动作在同一条 shell 链里无依赖检查。

## 修法（无损恢复序列，2026-09-06 实战验证）
1. **字节等价核验先行**：`git diff <分支尖端> -- <残留文件>` 为空 → 残留=分支内容的重复，无信息可丢
2. 冲突以 `checkout --ours -- <文件>` 定侧解决（digest 类自动生成文件取 HEAD 侧）
3. **保全性 stash**（`stash push -m`）代替 reset --hard 丢弃——权限拦截破坏性操作时这是正路
4. 本地领先 commit（如 digest 自动提交）`rebase <分支尖端>` 后再 push
5. 重部署后**必须验证产物版本**：`git merge-base --is-ancestor <期望尖端> HEAD` + 生产 RPC 实测关键值

## 证据
2026-09-06 部署树事故链完整走通：错树残留（=f0f172f 字节等价）+stash pop 冲突+误部署旧代码 → 核验/保全/rebase/重部署/RPC 验证（年化 20.07% 实证）。

## 反证条件
残留与分支尖端 diff 非空（含独有内容）时不得走保全性丢弃路线，须先回施工方确认内容归属。

## 治本
每席独立 worktree + seat 分支 + master 仅经 rebase+ff 收编（D-243/D-246，本条为过渡期恢复手册）。
