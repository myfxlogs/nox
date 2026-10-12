---
schema_version: 2
id: prompt-lifecycle-must-anchor-code-position
kind: pitfall
risk: high
status: verified
keywords: [施工提示词, 生命周期, 作用域, for内for外, 歧义, accumulator, 每轮新建, 长生命周期, 覆盖写]
scope: [workflow, agent-collab]
summary: 提示词写变量生命周期只用自然语言有二义——施工方默认取最小作用域（每轮新建）与设计的长生命周期相反，覆盖写 upsert 下退化为末轮快照；必须锚定代码位置
first_seen: 2026-08-22
last_verified: 2026-08-22
skills: []
---

# 施工提示词的生命周期约束必须锚定代码位置

## 症状

D-080 修复⑬ R2 返修：accumulator 类实现完全正确（当日累计+不清零+日切 pending），但 engine.go 三处接线把 `acc := newComboAccumulator()` 放在循环体内每轮新建、轮末 flush 后丢弃。覆盖式 upsert（`samples=EXCLUDED.samples`）下，`combo_daily_rollup` 日行被每轮覆盖成末轮单样本快照（samples=1、avg=min=max=瞬时值），当日 Σ/count/min/max 全丢。

## 根因

返修提示词原文「swapArbLoop / meanRevertLoop / oppLoop **各持局部** `acc := newComboAccumulator()`，**每轮 scan 内 Record，轮末 Flush**」——「各持」与「每轮」并存，生命周期无代码位置锚点。施工方合理地取了最小作用域读法（循环体内声明）。设计 SSOT 的「三个 loop 各持独立 accumulator（flush 后不清零，UTC 日切才重置）」只有在 for 外声明才能成立，但提示词没有把这一点翻译成代码位置。

**放大器**：类实现与接线分离时，类的单测（同实例跨 flush 累计出 5 样本）全绿——测试验证的契约生产接线根本不满足，机检无法发现，只能靠复审整读接线。同类风险=「每轮新建实例+幂等/覆盖语义的下游写入」组合：单轮内看似正确，跨轮状态（累计/去重/冷却）静默丢失。

## 修法（写提示词侧）

1. 生命周期约束一律给代码位置锚点：「上移到 `for {` 之前（`defer ticker.Stop()` 之后）」而不是「持有一个实例」。
2. 涉及「跨轮存续状态 + 覆盖写」的设计，提示词里显式写反例：「若每轮新建，日行会退化为末轮快照——禁止」。
3. 验收判据绑定数据形状而非存在性：不写「rollup 有行」（每轮新建也有行），写「samples 随扫描轮次增长，恒=1 即回归」。

## 证据

- 返修包②提示词：docs/修复⑬返修包②-施工提示词.md R7（含本教训留痕与代码位置锚定修法）。
- 反证测试：internal/autotrade/accumulator_test.go TestAccumulator_CumulativeOverwrite（同实例两次 flush → samples=5）。
- 设计定稿：docs/机会组合键贯通与验证闭环方案.md §5「三个 loop 各持独立 accumulator……flush 后不清零」。
