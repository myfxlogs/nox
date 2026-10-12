---
schema_version: 2
id: rework-red-proof-at-prefix-commit
kind: pattern
risk: low
status: verified
keywords: [返修, 复验, 先红后绿, 断言, 测试移植, worktree, 修复前 commit, 断言效力, 假绿]
scope: [复审流程, 返修验收, 测试断言]
summary: 返修 commit 里修复与新测试同 commit 时，「先红后绿」无法从绿结果自证——复审方把新测试文件移植到修复前 commit 的临时 worktree 独立跑红，才证明断言有牙（能失败）；绿测试+绿修复同批提交时未证红 = 断言效力未验收
first_seen: 2026-09-06
last_verified: 2026-09-06
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: 43dafdf
  - type: doc
    ref: docs/cnfi/D-247-P1返修复验-2026-09-06-Claude.md
---

## 事实/模式

D-247 P1 返修（2026-09-06）：R-F1 修复与 R-F2 消费侧断言（TestD247_BreakCrossIntervalMissing，含 N=1 断言）在同一返修 commit。施工方 commit message 声称「先红后绿（修复前 N=2 红）」，但交付物只有绿状态。复审方在修复前 commit（a9a67bd）建临时 worktree、仅移植测试文件后复跑——`FAIL: A N … want 1`（N=2 计入了不参与者）——再于返修 commit 复跑同测试 PASS。红/绿两点都由复审方独立产生，断言效力才成立。

## 根因或策略解释

「先红后绿」的目的不是仪式，是证明测试**有能力失败**（有牙）。修复与测试同 commit 时，交付树只存在「都修好」的世界，绿结果与「断言恒真/断言没跑到目标路径」不可区分。施工方的口头/消息声称无法替代机械证据；且断言是否命中所防御的路径（而非别的断言掩护过）只有让修复退场一次才知道。

## 处置（复审操作法）

1. `git worktree add --detach /tmp/<rev>-redcheck <修复前commit>`（共享仓多树下勿动他席树）。
2. 仅复制返修新增/修改的测试文件到临时树对应路径（勿带修复代码）。
3. 以同一 DSN/环境跑目标测试：预期 FAIL 且失败信息指向被修缺陷（本例 `A N … want 1`，非编译错/非别的断言错）。
4. 回到返修 commit 复跑同测试：预期 PASS。红绿失败/成功点都记录进复验文档。
5. 清理临时 worktree。注意：若测试依赖同 commit 的 gen/proto 变更，此法不适用（会编译失败），此时要求施工方提供红态证据或由复审方在修复 commit 内临时 revert 修复段证红。

## 证据

- check: `git worktree add --detach /tmp/d247rf2 a9a67bd && cp <返修树>/service_cnfi_d247_test.go /tmp/d247rf2/internal/dashboard/ && ARB_TEST_PG_DSN=… go test -run TestD247_BreakCrossIntervalMissing ./internal/dashboard/`
- result: a9a67bd+返修测试 → `FAIL service_cnfi_d247_test.go:146: A N … want 1（R-F1：缺失指标不计入 N）`；43dafdf → PASS
- ref: docs/cnfi/D-247-P1返修复验-2026-09-06-Claude.md §1 R-F2 行

## 反证条件

- check: 返修/新断言类施工在 commit 内附红态输出（CI 或截图/日志），或测试框架提供 mutation 验证
- falsifies_when: 后续返修交付自附红态证据且复审抽查复现，复审不再需要代跑红态

## 适用边界

修复与新测试同 commit 的返修复验、以及任何「新增断言」的效力验收（断言是否真能失败）。不适用于纯修复无新测试（无从证红，改用复审临时实证复现缺陷）或测试依赖同批 schema/gen 变更的场景（用 revert 修复段法）。
