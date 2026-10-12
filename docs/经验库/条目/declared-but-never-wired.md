---
schema_version: 2
id: declared-but-never-wired
kind: pitfall
risk: high
status: verified
keywords: [接线, 装配, 死代码, 注释声称, SetXxx, 零调用, fail-open 假象, wiring]
scope: [exec, cmd, autotrade]
summary: 接口/配置「声明了」不等于「接线了」——注释自称「main 注入」的 verifier 全仓零调用（SetPositionVerifier 零调用点），fail-open 老洞长期被当已修复；审计复审不能只查声明，必须 grep 调用点实证
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/exec/exec.go:62（PositionVerifier 注释「main 注入」）
  - type: doc
    ref: internal/exec/pipeline.go:76（SetPositionVerifier 零调用点）
  - type: server_fact
    source: 审计复审纪要 2026-08-23 §4.3
    query: verifier 未接线 fail-open 被列为待修；D-108 P1 复审时 grep 证实从未接线
    observed_at: 2026-08-25
---

## 事实/症状

- `PositionVerifier` 接口注释自称「实现者：reconcile.VenueQuerier 适配器（main 注入）」——审计（8-23）把「verifier 未接线 fail-open」列为待修；**D-108 P1 复审（8-25）grep 实证：`SetPositionVerifier` 全仓零调用**，wiring.go 里还有个从未被引用的 `execPositionVerifier` 适配器（死代码）。注释给了两次「已接线」的假象。
- 同型：`ScanThrottle` 配置字段存在两版（config+engine）却从未参与任何判定（死配置）；D-107 P4 才接活。

## 根因或策略解释

- **声明/定义 ≠ 装配**：接口存在 + 注释说注入 ≠ 生产路径真的调用了 setter。审查者（包括审计）看注释下结论，两次漏过。
- fail-open 的默认值恰好无害（nil=跳过检查），所以「未接线」能长期存活不炸——静默降级比报错更难发现。

## 处置

- 复审接线类修复时：**grep 调用点实证**（`SetXxx` 全仓引用计数），不采信接口注释/构造器参数。
- 防御性编程：nil 依赖的降级路径要有告警（如 verifier nil → 告警而非静默跳过——D-108 A2 已改为 fail-closed）。
- 死配置排查法：配置字段 grep 消费点，零消费=死配置，接活或删除。

## 证据

- check: D-108 P1 复审 grep：SetPositionVerifier 仅定义处 1 处、零调用；wiring.go execPositionVerifier 未引用
- result: 返修 R1 新 pipelineVerifier 接线（hook 内 set + pipeline 创建后注入）；A2 fail-closed 生效
- ref: 会话 2026-08-25（D-108）

## 反证条件

- check: 再次 grep 同类 setter 调用点
- falsifies_when: SetPositionVerifier 出现非测试调用点且行为实证

## 适用边界

- 一切「依赖注入/接线」型修复的复审；审计「fail-open」类项的根因核查。
- 相关：[[construction-claim-needs-machine-check]]、[[unspecified-err-handling-defaults-unsafe]]。
