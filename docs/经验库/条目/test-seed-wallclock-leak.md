---
schema_version: 2
id: test-seed-wallclock-leak
kind: pitfall
risk: medium
status: verified
keywords: [测试污染, 墙钟种子, now()-相对日, 清理哨兵, 持久库, 本地红 CI 绿, AddDailyIncome, 累加 upsert, 泄漏行]
scope: [Go 测试, DB 种子自洁, 持久测试库 vs CI 全新库]
summary: 测试种子日=now()-相对值随墙钟漂移+清理哨兵用固定远期日 → 持久库每跑一次泄漏一行，第二次运行断言值精确翻倍；CI 全新库必绿=本地红/CI 绿陷阱；修法=清理谓词用测试专属标记列
first_seen: 2026-09-07
last_verified: 2026-09-07
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: bed00e5
  - type: doc
    ref: internal/dashboard/service_hedge_d236_test.go:450
  - type: doc
    ref: docs/D-249中检点复审-S1-S3-2026-09-07-Claude.md
---

## 事实/症状

TestD236_v14_CarryAnnualizationMath 本地（DSN 指向持久库）第 2+ 次运行必红：
`cum = 2429, want ≈1214.5`（精确 2×）。CI（每次全新 postgres service）恒绿。
与 [[wallclock-base-periodic-bucket-flaky]] 同族但机制不同：不是断言相位相关，是**数据泄漏**。

## 根因或策略解释

三个因素叠加：
1. 种子日=「now−10d」——随墙钟漂移，每次运行落不同日期行（9-6 跑留 08-27，9-7 跑留 08-28）
2. 清理哨兵=`DELETE WHERE day >= '2099-01-01'`——永不匹配种子行（种子日根本不是远期哨兵日）
3. cum 计算读全表（FirstDailyIncomeDay 取最旧行定 days）——每泄漏一行 sum 增一份种子值

泄漏行数=历史运行次数，失败值=泄漏行数×种子值，恒为整数倍。区分于一次性污染：
「精确 N 倍」是泄漏型污染的指纹。

## 处置

- 立即：按测试专属标记清残留（本例 note 列='d236v14'，注意 source 列是别处写入的别被误导）
- 根修（指令级，归 S6）：种子前+`t.Cleanup` 都按标记列删（`DELETE WHERE note='d236v14'`），
  或种子用固定远期哨兵日使既有清理谓词生效
- 审查口诀：**种子时间戳含 now() 的测试，清理谓词必须按测试标记（note/source）而非日期**

## 证据

- check: `DELETE WHERE note='d236v14'`（清 2 行=9-6+9-7 各一次）后重跑
- result: dashboard 全脸 ok（3.0s），TestD236_v14 PASS
- check: CI run 34082483146（同内容全新库）
- result: 该测试绿（CI 红=另一瞬断 EBADF，非本因）

## 反证条件

- check: 连续 3 天每天各跑一次不清残留
- falsifies_when: 三天行数仍 1 → 种子非 now()-相对（另寻根因）

## 适用边界

- 适用于一切「种子值含 now()/相对时间 + 库可跨运行存活（本地 DSN/长命容器）」的测试
- 不适用于 CI-only 全新库（每次 run 无历史）；但同一测试本地必炸——新测试落地时应问
  「这个测试在持久库上连跑两次绿吗」
