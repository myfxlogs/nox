---
schema_version: 2
id: unspecified-err-handling-defaults-unsafe
kind: pitfall
risk: high
status: verified
keywords: [fail-open, fail-closed, 错误处理, 风控查询, err 忽略, 设计缺口, 安全默认]
scope: [exec, workflow, agent-collab]
summary: 设计未规定风控类查询的错误处理方向时，施工默认忽略 err（fail-open 放行）——设计必须显式声明安全检查 fail-closed
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: decision
    ref: D-072 实施修正②
  - type: doc
    ref: internal/autotrade/gate.go Submit fail-closed 注释
---

## 事实/症状

D-072 gate 施工版对 GetAutotradeState/IsStrategyDisabled/DailyRealizedPnl/CountOpenPositions 四项风控查询的 err 全部 `_` 忽略——store 故障时状态默认 RUNNING、禁用默认 false、日亏默认 0、持仓默认 0，**全风控旁路放行**。

## 根因或策略解释

设计文档与施工提示词均未规定 err 策略（双方缺口）。施工方在自由裁量时选了「查询失败继续执行」的宽松方向——风控/安全类检查的正确默认是反方向：查不到状态 = 拒绝。

## 处置

① 设计文档对每个外部依赖调用显式写错误方向（风控/鉴权/状态类 = fail-closed，展示类 = 降级显示）；② gate 修正为任一检查失败即 REJECT 并落 autotrade_logs；③ 复审清单增加「err 处理方向」专项。

## 证据

- check: `grep '_ = g.store\|, _ :=' internal/autotrade/gate.go`（修正前）
- result: 4 处风控查询 err 忽略；修正后任一 err → REJECT
- ref: D-072 实施修正②

## 反证条件

- check: 审查 gate 当前实现
- falsifies_when: 出现「查询失败仍应放行」的合法场景（如降级只读模式）→ 收窄 scope 并显式列出例外

## 适用边界

所有风控/安全/状态判定路径。展示与日志路径不适用（降级渲染合理）。
