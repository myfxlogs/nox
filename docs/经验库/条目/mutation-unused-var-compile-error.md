---
schema_version: 2
id: mutation-unused-var-compile-error
kind: pitfall
risk: medium
status: verified
keywords: [变异测试, mutation, 编译错, declared and not used, build failed, 假红, 假阴性, Go, 脚手架]
scope: [复审流程, 变异对抗, Go 测试]
summary: Go 变异「摘除代码块」若同时摘掉某变量的全部使用点 → `declared and not used` 编译错，测试摘要呈 "0 passed [build failed]"——这是假红不是断言红，变异效力未证。修法=块内留 `_ = var` 脚手架保编译，让变异只改行为不破编译；跑后必须核对失败明细是断言不等而非 build failed。
first_seen: 2026-09-18
last_verified: 2026-09-18
skills: [验证与交接]
evidence_refs:
  - type: doc
    ref: docs/handoff/LOG.md（2026-09-18 D-325+D-326 复审通过收编部署段 + 2026-09-17 施工送审段流程教训）
  - type: commit
    ref: a2b4a4de
---

## 事实/症状

D-325 复审变异对抗两轮各踩一次：

- M1（摘 native swap 双发块）：首次直接删块 → `internal/adapter/mt_swap.go:124:4: declared and not used: rawMode`，测试摘要 "0 passed, 1 failed [build failed]"——看似「转红」实为编译错，断言根本没跑到。
- M2（native 改用 override 后 mode）：同样使 rawMode 失去全部使用点 → 同型编译错。

施工方 9-17 已踩过同类坑（perl 单行变异截断签名/产编译错假阴性），LOG 两次留痕「编译错≠转红」。

## 根因或策略解释

Go 编译器把未使用变量当硬错误；「摘块」类变异天然容易连带移除某变量的最后使用点。此时 `go test` 连包都编不过，红信号来自编译器而非断言——不能证明锚对该变异灵敏。有效变异必须「只改行为、不破编译」，让测试跑到断言层失败。

## 处置

1. 摘块类变异后检查被摘块是否含某变量的最后使用点；若是，在残留位置留 `_ = varName` 脚手架（如 `if brokerSym != canon { _ = rawMode }`）。
2. 跑测试后必须读失败明细：断言不等（`got X want Y`）才是有效红；`build failed` / `declared and not used` = 变异无效，重做。
3. 变异与还原一律用 Edit 工具精准替换，禁 perl 单行（两次截断/假阴性实证）。

## 证据

- check: M1 脚手架版（`_ = rawMode`）→ `TestD325_SwapFacts_DualEmission`/`ExnessNativeRaw` 断言红（native 计数 0/缺失）；M2 脚手架版 → T3 红 `native swap_long = 0.0000, want -146.5479`
- result: 两轮均为断言级红，还原后全绿、工作树与 HEAD 一致
- ref: LOG.md「2026-09-18 D-325+D-326 复审通过收编部署」段；merge=a2b4a4de

## 反证条件

- check: 找一个摘块变异不移除任何变量使用点的场景
- falsifies_when: 该场景下无需脚手架——成立，本条目只适用于「摘块连带移除最后使用点」的情形；编译错红若能证明与断言红等价（理论上不能，断言未执行）则条目失效

## 适用边界

Go 项目（编译器未使用变量硬错误）；其他语言变异按各自编译/解释语义类比（核心不变量：变异失败形态必须来自断言层，非工具链层）。
