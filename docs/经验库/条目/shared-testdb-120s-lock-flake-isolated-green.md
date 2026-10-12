---
schema_version: 2
id: shared-testdb-120s-lock-flake-isolated-green
kind: pitfall
risk: high
status: verified
keywords: [共享测试库, flake, 120s, 120.00s, 整点超时, 锁等待, 孤立绿, 整包跑, 逐轮换测试, 并发任务, 复审机检, TRUNCATE 互踩, 同席并发, duplicate key, nil panic, dropped or renamed]
scope: [Go 测试, DSN 门控测试, 共享 PostgreSQL 测试库, 复审方法论]
summary: 整包跑 DSN 测试时 FAIL 恒为 120.00s 整且逐轮换不同测试、单跑同测试 0.03s 绿=共享测试库锁/内容竞争（常来自另一并行测试进程或后台残留任务），非代码红；甄别法=先孤立复跑失败测试、再 master 基线对照，两者绿/同红即归 flake 类不归施工；2026-10-04 补：复审席同 worktree 并行跑全量+race 两路 go test 亦自撞（库名含 worktree 后缀只隔离跨树不隔离同树多进程）
first_seen: 2026-09-18
last_verified: 2026-10-05
skills: [验证与交接]
evidence_refs:
  - type: decision
    ref: D-249
  - type: doc
    ref: docs/handoff/STATE.md D-327 行「radar 红+AppendNoteConcurrent 红=预存 flake 类（孤立绿）」
  - type: doc
    ref: internal/dashboard/service_cnfi_progress_test.go:61
  - type: decision
    ref: D-060 复审实录 2026-10-04（同 worktree 全量+race 并发自撞，串行重跑定论）
---

# 共享测试库 120s 整点 flake：孤立绿甄别法

## 症状

整包（`go test ./internal/dashboard/`）跑 DSN 门控测试时出现 FAIL，且有三特征同时成立：
1. **耗时恒为 120.00s 整**（pgx/语句级锁等待超时的整点签名，非业务断言失败的毫秒级）；
2. **逐轮换测试**：同一包两轮跑，第一轮红 TestGetCNFIProgress_Math、第二轮红 TestD247_BreakCrossIntervalMissing——红的不是固定测试；
3. **孤立复跑 0.03s 绿**：单 `-run` 该测试立即绿。

2026-10-04 同席自撞新形态（D-060 复审实录）：并行两路 go test（全量+race）同 worktree 同发，症状扩为 duplicate key 违反唯一约束、nil pointer panic、`no rows in result set`、TRUNCATE lock timeout、恢复测试库被 DROP（`database "arb_restore_test_<worktree>" does not exist … just been dropped or renamed`）——失败包落在完全未触碰的域（cnfi/capital），且 `| tail` 管道吞退出码致任务显示 exit 0（假绿）。串行重跑全绿定论。

## 根因

多包/多任务共用同一测试 PostgreSQL（arb_test 等 `_test` 库）：另一并行 go test 进程（如同时跑的基线对照、notify 集成测试）TRUNCATE/迁移/插行与本包互踩，连接池拿到的锁等待到整点超时。测试间无隔离假设——任何「后台还挂着别的测试任务」都会复现。同族：D-249 migration 并发污染（并跑 2 红 master 隔离同绿）。D-021 的库名含 worktree 目录后缀只隔离**跨 worktree** 并发，同 worktree 内两个 go test 进程库名相同仍 100% 互撞——「隔离靠后缀」不等于「隔离靠进程」。2026-10-05 补第三形态：单命令 `go test -race ./pkgA/ ./pkgB/` 的**包级并行**（同进程内）同撞——多包 race 检查也要拆包跑或看孤立复跑。

## 修法（甄别流程，非修测试）

1. 孤立复跑：`go test -run '<失败测试>' -count=1` ——绿 ⇒ 与代码无关；
2. master 基线对照（stash 施工改动跑同集）——同红/同样 flake ⇒ 预存类；
3. 归档为预存 flake 在报审说明中显式登记（哪几个测试、哪轮、孤立绿证据），**禁止为转绿改测试或改共享库**。
真正的代码红（如先红后绿锚）特征相反：固定测试、毫秒级断言失败、孤立复跑仍红。
**预防**：同席位机检多路任务（全量/race）串行跑，不并行发两个 go test；判断成败禁走 `| tail` 管道（退出码被吞），用输出重定向+`echo EXIT=$?`。

## 例外

若孤立复跑仍红且 master 绿 ⇒ 真回归，按先红后绿锚排查。
