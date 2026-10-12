---
schema_version: 2
id: pipe-swallows-go-test-exit-code
kind: pitfall
risk: high
status: verified
keywords: [go test, 管道, 退出码, grep, tail, 全量验证, 假绿, 复审, bash]
scope: [复审验证, 机检, CI, bash]
summary: 验证命令接管道（grep/tail/head）后 $? 是管道末命令的退出码——go test 全量失败被报成「全绿」，D-190 一审据此误判通过并漏开 dashboard 退回项
first_seen: 2026-09-02
last_verified: 2026-09-03
skills: [验证与交接, KB维护]
evidence_refs:
  - type: decision
    ref: D-190
  - type: commit
    ref: fcff177
  - type: doc
    ref: docs/D-190复审退回清单.md:R7
---

## 事实/症状

D-190 一审复审用 `go test ... ./... | grep -v '^ok' | tail -5; echo exit=$?` 验证全量——输出只见 binance-proxy 缓存噪音，判「全量通过」。二审重验发现 dashboard 包当时已确定性 FAIL（TestCheckSwapDeviationGate_HighDeviation_Rejected），其 FAIL 行被 `tail -5` 截掉、真实退出码被管道吞掉，`exit=0` 是 tail 的。

## 根因或策略解释

bash 管道默认 `$?`=末命令退出码；`grep -v`/`tail -N` 既吞退出码又截断输出（FAIL 行可能在被截段）。两条叠加：非零退出不可见+失败明细不可见=「假绿」。

## 处置

全量/包级验证一律二选一：
1. 落文件后查：`go test ... > /tmp/x.log 2>&1; echo exit=$?; grep -c '^--- FAIL' /tmp/x.log`（exit 用无管道环境取）
2. 或 `echo ${PIPESTATUS[0]}`（bash）取管道首命令退出码
另外：DSN-gated 测试的证据要求必须列全包名并给出精确 `-run` 模式——autotrade 的 `TestSwapDeviationGate*` 与 dashboard 的 `TestCheckSwapDeviationGate*` 是两套测试，只写一侧=另一侧静默 SKIP/失败漏检。

## 证据

- check: `go test -count=1 ./internal/dashboard/ > /tmp/dash.log 2>&1; grep '^--- FAIL' /tmp/dash.log`
- result: `--- FAIL: TestCheckSwapDeviationGate_HighDeviation_Rejected`（D-190 返修提交上确定性复现，一审未见）
- ref: D-190 二次退回 R5/R7；commit fcff177

## 反证条件

- check: 在 bash 下 `go test ./不存在包/ 2>/dev/null | tail -1; echo $?` 验证退出码语义
- falsifies_when: 若 shell 配置使 `$?` 反映管道首命令（如 set -o pipefail + 显式取 $?），且输出无截断——则本条目前提失效

## 适用边界

适用于一切「命令成功与否靠退出码判定」的验证场景（go test/build/vet、CI 脚本）；不适用于明知只看文本输出的 grep 用法。pipefail 只改退出码不改输出截断——tail 截断风险独立存在。


## 9-7 复发补充（三种新变体，同根因）

1. `git merge --ff-only … | tail -1 && push`——merge 冲突失败被 tail 恒 0 掩蔽，把不相关 digest 提交推上远端
2. `grep -n x f | head -1 || echo >> idx`——head 恒 0，`||` 兜底分支永不执行（索引行静默丢失）
3. 探针 `go test … | grep FAIL | head` 把 FAIL 文本当正常输出阅读，全程 exit 0，失败证据被「顺利读完」

**后果分级**：变体 1 最重（假 0 后面跟着外向动作 push）；验证/阅读类（变体 3）只损失证据质量。
修法一致：后果性链路禁管道（`if cmd; then`）或 `${PIPESTATUS[0]}`；「EXIT=$?」要独立成行紧跟目标命令。
