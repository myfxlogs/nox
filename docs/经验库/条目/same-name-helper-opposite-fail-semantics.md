---
schema_version: 2
id: same-name-helper-opposite-fail-semantics
kind: pitfall
risk: high
status: observed
keywords: [isRealPair, fail-closed, fail-open, 语义不一致, 同名 helper, IsStrategyDisabled, DB 故障, 行为不可预测]
scope: [autotrade, detector, gate, DB 故障处理]
summary: isRealPair 在 autotrade 包 fail-closed（未知→拒绝）、在 detector 包 fail-open（未知→放行）；IsStrategyDisabled 在 gate fail-closed、在 trySwapArb 预检 fail-open——同名 helper 不同包语义相反，DB 故障时行为不可预测。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/autotrade/engine_swaparb_isreal.go:20
  - type: doc
    ref: internal/detector/detector_isreal.go:6
  - type: doc
    ref: internal/autotrade/gate.go:75
  - type: doc
    ref: internal/autotrade/engine_swaparb.go:109
---

## 事实/症状

D-188 地基审计发现两对同名 helper 语义相反：

**isRealPair**：
- `autotrade/engine_swaparb_isreal.go:20`：realMap 缺失/nil → `false`（fail-closed，拒绝配对）
- `detector/detector_isreal.go:6`：realMap 空/缺失/nil → `true`（fail-open，放行配对）

**IsStrategyDisabled**：
- `gate.go:75`：查询失败 → `REJECTED`（fail-closed）
- `engine_swaparb.go:109`：`if disabled, _ := ...`（err 被 `_` 忽略，disabled=false，fail-open）

**后果**：DB 故障时（如 `ListAccountIsReal` 查询失败）：
- detector 放行所有组合（fail-open）
- autotrade discoverSwapCombos 拒绝所有组合（fail-closed）
- 行为取决于哪个先执行，不可预测

## 根因或策略解释

D-178 的设计意图是"检测层 fail-open + gate 层 fail-closed"——检测层宽松放行，gate 层严格兜底。但 `discoverSwapCombos` 属于 autotrade 包却用了 fail-closed 版 isRealPair，导致"发现层"比"检测层"更严格，与设计意图矛盾。

`IsStrategyDisabled` 在 trySwapArb 预检中用 `_ =` 忽略错误，是因为"gate 会兜底"——但如果 gate 也查询失败（fail-closed 拒绝），trySwapArb 已经产了 intent 浪费资源。

## 处置

**待修复（D-190 P1-2）**：
1. 统一 isRealPair 语义——明确 discoverSwapCombos 属于"发现层"还是"执行层"，对应选择 fail-open 或 fail-closed
2. trySwapArb 预检的 IsStrategyDisabled 改为检查 err（fail-closed），或删除预检（依赖 gate 兜底）
3. 修正 `engine_swaparb_discover.go:74-75` 注释（标注 fail-open 但实际 fail-closed）

> 注：本条目 status=observed（已观察未修复），修复完成后升 verified 并补"修复实证"到证据节。

## 证据

- check: `grep -rn 'func isRealPair' internal/`
- result: 两个包各一个，语义相反
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认 D-178 设计文档是否明确 discoverSwapCombos 的 fail 语义
- falsifies_when: 如果 D-178 明确要求 discoverSwapCombos 用 fail-closed（作为执行层前置过滤），则语义一致（但注释需修正）

## 适用边界

适用于所有 DB 查询失败场景。正常运行时（DB 可用）不受影响——只有 DB 故障时语义差异才显现。
