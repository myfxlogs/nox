---
schema_version: 2
id: shared-teststore-flag-leak-and-import-cycle
kind: pitfall
risk: medium
status: verified
keywords: [共享测试 Store, import cycle, in-package test, SetAlertPaused, 实例级开关, flags 泄漏, 顺序依赖, 孤立绿 全包红, storetest 子包, sync.Once]
scope: [Go 测试, 共享 fixture/共享池改造, DSN 门控测试]
summary: 把 per-test 的 pgstore.New 改成包进程级共享 Store 时两个必踩坑——①Store 实例级易变开关（SetAlertPaused/SetAlertHook 类）会跨测试泄漏：某测试置 paused=true 后后续测试 InsertAlert 静默丢弃，表现为「孤立绿+全包确定性红」的顺序依赖（与 flake 区别：每次全包都红）；修法=共享获取口统一复位易变 flags。②被测包自身的 in-package 测试不能引用 import 了该包的 helper 包（Go 禁 import cycle in test）——共享 helper 拆独立子包，被测包包内就地同款单例
first_seen: 2026-09-19
last_verified: 2026-09-19
skills: [验证与交接, KB维护]
evidence_refs:
  - type: commit
    ref: c3d63568
  - type: doc
    ref: internal/store/pgstore/shared_test.go（获取口复位+cycle 说明）
  - type: doc
    ref: internal/testutil/storetest/storetest.go（子包拆分）
---

## 事实/症状

- 共享 Store 改造后（D-351 S1）：pgstore 全包跑时 TestPurgeAckedAlerts/TestAlreadyAlertedToday 确定性红（purge count 0/remaining alerts 0），孤立跑/两两组合跑全绿。根因=先跑的 opportunities_alert_test 调 `SetAlertPaused(true)`（D-069 paused 时 InsertAlert 静默丢弃），flag 留在共享 Store 实例上泄漏给后续全部测试——per-test 池时代该 flag 随实例自灭，共享化把「实例级状态」变成「进程级状态」。
- pgstore 自身 in-package 测试引用共享 helper 包（helper import pgstore）→ `import cycle not allowed in test` 编译拒绝。

## 根因或策略解释

共享化改变的不只是 dial 数，还有**对象生命周期**：原来每测试一个 Store 实例，实例上的非 const 字段（paused/hook/缓存 map 等「易变状态」）天然按测试隔离；共享后这些字段变成隐式跨测试通道。凡「获取共享对象+测试体内 set 开关」的测试都依赖这个隐式语义。

## 处置

1. 共享获取口统一复位易变 flags（每次获取都复位，缓存命中路径同样要过）——需要的测试在自己的测试体内重新设置（现有测试正是「先取 store 再 set」顺序，天然兼容）。
2. helper 放独立子包（如 `internal/testutil/storetest`）供外部测试包消费；被测包自身的 in-package 测试用就地同款单例（机制一致：同 DSN 同指针+获取口复位），并在 shared_test.go 注释里写明 cycle 因果。
3. 甄别顺序依赖 vs flake：孤立绿+全包**每次都红**=顺序依赖（真问题）；孤立绿+全包**时红时绿**=共享资源竞争 flake（见 shared-testdb-120s-lock-flake-isolated-green）。

## 证据

- check: `go test ./internal/store/pgstore/`（全包）修复前每次红 TestPurgeAckedAlerts/TestAlreadyAlertedToday；`-run` 孤立绿；复位后全包绿（commit c3d63568 机检）
- check: pgstore 测试引用 testutil 主包版共享 helper → `import cycle not allowed in test`；拆 storetest 子包+包内单例后 build/vet 净
- ref: seat/zcode-d351 @c3d63568

## 反证条件

- check: 若未来 Store 新增实例级易变字段而未纳入获取口复位清单，同签名红会复现
- falsifies_when: 共享 Store 改为每测试重建实例、或 Go 放宽 test import cycle 限制

## 适用边界

- 适用于「per-test 实例 → 进程级共享实例」改造；实例级**只读**字段不在此列；跨测试的数据隔离仍由各测试既有 TRUNCATE/前缀 DELETE 自持（本条不覆盖数据面）。
