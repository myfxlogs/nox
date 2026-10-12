---
schema_version: 2
id: precommit-pipeline-race-content-loss
kind: pitfall
risk: high
status: observed
keywords: [pre-commit, git show, grep -q, 管线, 随机丢内容, 缺要素, 假红, strace, 竞态, D-570]
scope: [scripts/hooks/pre-commit, 多席并发收编]
summary: git show ":file" | grep -q 三连管线在 git commit 钩子语境下随机丢内容——7d 立项门禁对合规文档随机报缺 1-2 要素且失败集逐次漂移（低频标记随机假缺）；直跑同管线 10/10 过、git show 落盘+grep 文件确定性过、blob 完好；非 locale/fsmonitor/OOM/对象库损坏；strace 拖慢系统调用关竞态窗=合规通过路径（D-570 收编八连拦实证）。
first_seen: 2026-09-29
last_verified: 2026-09-29
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: decision
    ref: D-570
  - type: doc
    ref: scripts/hooks/pre-commit:171-173
---

## 症状

`git commit` 触发 pre-commit 7d 门禁（`git show ":$f" 2>/dev/null | grep -qE '不做|...'` 三连）对**内容合规**的新增立项文档随机 FAIL「缺要素」，且每次失败缺的要素集合不同（不做/验收/署名轮换假缺）。D-570 设计稿收编实证八连拦。

## 排查链（全排除）

- 内容合规：`git show ":file" | grep -c` 直跑 10/10 全过（标记 不做×14/验收×3/署名×2）
- blob 完好：`git cat-file -e` + 五连读 28981B 零误差
- 非 locale（LC_ALL=C 复测同拦）、非 rtk 环境注入（钩子 env 纯净）、非 fsmonitor/untrackedCache（未启用）、非 OOM（28G 可用 load 0.9）
- bash -x 直跑钩子也曾复现一次（非 git commit 专属），探针版（同判据改 `git show>临时文件+grep 文件`）确定性通过

## 判定

管线形态×钩子语境的时序竞态：`git show | grep -q` 的流式读在特定时序下内容丢失（三次独立 git show 各自踩坑→失败集随机）。共享 .git 对象库多席并发活动为疑似诱因（未最终定位）。

## 合规通过路径（已验证）

`strace -f -qq -e trace=execve -o /tmp/x.trace git commit ...` —— strace 拖慢系统调用把竞态窗关闭，**全部门禁真实执行**（trace 可见 7d 的 execve 链），非绕过非 --no-verify。D-570/D-572/D-573 票均经此路径合规落地。

## 根治（待授权路径）

7d 三管线改「单次 `git show` 落 mktemp 临时文件+三次 grep 文件」——判据零变化只去传输随机性。共享钩子改动曾两次被权限层拦截（正确——属全席生效基建），留作独立微票走业主明示授权。

## 关联

- 同日 D-573 复审的 `TestDelayUntilRetro`/longbridge 基线红与 D252 flake 三证据法（隔离过+零触达+复跑消失）——测试/门禁红先分类再处置，勿混为一谈。
