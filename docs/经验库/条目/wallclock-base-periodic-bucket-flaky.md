---
schema_version: 2
id: wallclock-base-periodic-bucket-flaky
kind: pitfall
risk: high
status: verified
keywords: [flaky, time.Now, 墙钟, 基点, 小时桶, 周期桶, 去重, Unix()/3600, 注入时钟, 分时失败, 先红后绿假绿]
scope: [Go 测试, 注入时钟, 告警去重/周期分桶逻辑, 复审方法论]
summary: 测试用 time.Now() 做注入时钟基点+runtime 按周期桶（now.Unix()/3600）去重 → 基点分钟 ≥50 时 +10min 用例跨桶必败（16.7% 时间窗 flaky）；修法=固定基点且分钟 <50，负探针钉 :55 证机制
first_seen: 2026-09-06
last_verified: 2026-09-07
skills: [验证与交接]
evidence_refs:
  - type: commit
    ref: 82fa1f2
  - type: doc
    ref: internal/reconcile/close_governance_test.go:17
  - type: doc
    ref: docs/D-234批1复审-2026-09-06-Claude.md
---

## 事实/症状

TestD245_ClosingAlertTiering / TestD245_UnknownTiering 偶发 FAIL（warn=2 want 1 / warn=3 want 2）：
14:50 UTC 跑必红、其它时段绿；CI 面上无规律间歇红（6cb756b push CI=failure 即此）。
无 race、无并发、单独重跑又绿——典型分时 flaky。

## 根因或策略解释

测试用 `base := time.Now()` 做注入时钟基点；runtime 告警去重按小时桶
`r.now().Unix()/3600`（reconcile_close.go:275）。基点分钟 ≥50 时，用例推进
+10min/+70min 跨入下一小时桶 → 去重键变化 → 同层告警计数翻倍 → 断言 FAIL。
失败窗口 = 分钟 ∈ [50,60) ≈ 16.7%。D-245 引入时先红后绿恰好跑在分钟 <50 安全窗，
绿得真、错得也真——时间维度未审=复审漏网。

## 处置

1. 基点固定化：`var d245Base = time.Date(2026, 1, 5, 0, 5, 0, 0, time.UTC)`（分钟 <50，
   给 +10min 用例留同桶余量），全部时间戳（stuckAt/持仓 opened_at/nowFn）统一相对该基点
   ——漏改任一处会致 stuckFor 为负、分层前提崩
2. 负探针证机制：把基点临时钉 `:55` → 必 FAIL（warn 翻倍）→ 还原 :05 → 稳定绿；
   `-race -count=3` 复跑
3. 复审守则：审测试必问「断言是否墙钟相位相关」；凡注入时钟+周期桶组合，先红后绿
   的「红」须能由相位移动确定性复现，否则先红无效

## 证据

- check: 基点钉 `0,55` 跑 TestD245_UnknownTiering
- result: FAIL「UNKNOWN 分层: critical=0 warn=3, want 0/2」（跨桶多 1 条，机制确定性）
- check: 基点 `0,05` 后 `go test -race -count=3 -run TestD245 ./internal/reconcile/`
- result: ok（1.06s）×3 稳定；整包 race 绿
- ref: commit 82fa1f2；CI 修复前 6cb756b=failure → 修复后 success

## 反证条件

- check: 在修复后代码上把 d245Base 钉回 :55
- falsifies_when: 全时段 PASS → 说明失败非跨桶机制（另寻根因）

## 适用边界

- 适用于一切「注入时钟 + 按周期分桶去重/聚合/限流」逻辑的测试（小时桶/日桶/窗口限频）
- 不适用于无桶化的纯差值断言（elapsed < X 类，另有时钟精度问题）
- 固定基点须选「离桶边界足够远」的分钟值，并给最大推进量留余量（本例 +70min 仍需跨桶——
  跨桶用例的期望计数须按跨桶后实际桶写，而非一律假设同桶）
