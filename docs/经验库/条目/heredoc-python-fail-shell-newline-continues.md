---
schema_version: 2
id: heredoc-python-fail-shell-newline-continues
kind: pitfall
risk: medium
status: verified
keywords: [heredoc, python, 断言失败, shell 换行续行, 非条件串联, git rebase --continue, 冲突标记, 未解决状态入库, git add, 复审]
scope: [多命令 bash 调用, git rebase 冲突解, 程序化改文档/代码, agent 终端]
summary: heredoc 内 python 以断言失败退出≠阻断后续命令——多行命令串里用换行（非 &&）衔接时，python 失败后 git add / git rebase --continue 照常执行，把带 <<<<<<< 标记的未解决文件提交进 commit；修复=断言型脚本与其后续 git 步骤必须 && 串联（或全部步骤收进脚本内），rebase --continue 后必验 grep 冲突标记零残留
first_seen: 2026-09-26
last_verified: 2026-09-26
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: 11765ba8（D-507 收编：标记短暂入 commit 后 amend 解净；amend 前后 grep 验证在会话）
  - type: doc
    ref: docs/handoff/LOG.md（2026-09-26 D-507 复审段「插曲」句）
---

## 事实/症状

- 复审收编时解 STATE.md rebase 冲突：命令串 = `python3 - <<'EOF'（含 assert）` **换行** `git add … && GIT_EDITOR=true git rebase --continue`。
- python 因锚文本失配断言失败退出非零——但换行衔接的下一条 `git add + rebase --continue` **照常执行**，把仍带 `<<<<<<< / ======= / >>>>>>>` 标记的文件 stage 并完成 rebase commit。
- 若未当步 `grep -c` 验证（工作树与 `git show HEAD:` 双查），标记随收编 commit 进 master 前沿。

## 根因或策略解释

- shell 换行只是语句分隔，无条件语义；heredoc 结束符 `EOF` 后的新行是一条独立命令，python 的退出码不构成任何门。
- 与 `cmd | tail 吞退出码`（pipe-swallows-go-test-exit-code）同族但更隐蔽：失败发生在「前置步骤」，掩盖发生在「后续步骤无条件放行」。
- python-replace-no-op-trap 防的是「未命中静默 no-op」；本条防的是「命中失败后 shell 续跑」——两层都要设防。

## 修法

- 断言型 python 脚本与后续 git 命令一律 `&&` 串联；或把 git 步骤收进 python 内（subprocess + 检查返回码）。
- 冲突解类操作后置验证为硬步骤：`grep -c '<<<<<<<\|>>>>>>>' <file>` 与 `git show HEAD:<file> | grep -c` 双零才放行。
- 解冲突优选行号手术（定位 marker 行区间再切片替换）而非长文本全等匹配——长锚在 rtk 转义/尾空格下易失配（本例首败即此）。

## 预防/触发条件

- 任何「脚本改文件 + git 收尾」的复合命令，检查衔接符：换行=无条件，`&&`=门。
- 程序化改共享 SSOT（STATE/decisions）时，write 前完成全部 assert（失败即零写入），写盘是最后一步。
