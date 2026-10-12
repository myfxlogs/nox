---
schema_version: 2
id: regression-anchor-encodes-bug
kind: pitfall
risk: high
status: verified
keywords: [回归测试, 锚, 期望值, fixture, venue 类型, 符号反向, funding, 测试反讽, 先红后绿, 变异测试]
scope: [单测设计, 函数级 DB fixture 测试, autotrade 退出判定]
summary: 回归测试把 bug 的错误行为写成期望值（want=longFunding−shortFunding 的 −6），且 fixture venue 类型与函数真实消费场景错位（vA:mt5/vB:mt5 腿测 funding 退出函数）——锚「绿」只证「实现与 bug 一致」，bug 带着全绿测试存活至 D-312。修法=锚值从规格推导（符号语义以源码佐证）+fixture 复刻真实 venue 形态+新锚对旧码先红后绿+变异对抗证灵敏度。
first_seen: 2026-09-17
last_verified: 2026-09-17
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-312
  - type: commit
    ref: 602a7fc7
  - type: doc
    ref: internal/autotrade/engine_position_test.go
---

## 事实/症状

`recalcNetFunding` 符号反向 bug（D-312：`longFunding−shortFunding`=−净收入，盈利仓误平）存活多个验收轮次，期间回归测试 `TestRecalcNetFunding_NormalBothVenues` 一直全绿——该锚断言 `want=6.0 (10−4)`，把错误符号的输出固化成期望值；且 fixture `seedSwapPosition` 用 `vA:mt5/vB:mt5` 腿测 funding 退出函数，与真实 PERP_FUNDING/FUNDING_HEDGE 持仓的 `:perp/:spot` 腿形态不符（结构性缺陷因此测不出）。

## 根因或策略解释

锚值来源错位：期望值从「实现当前输出」反推，而非从规格（funding rate 语义=正值多头付空方 → 净收入=short−long）正推——测试退化为实现的快照，bug 被写进回归保护。fixture 形态错位：测试桩的 venue 类型与函数真实消费场景不同构，函数的结构性分支（:perp 判定、spot 腿不参与）从未被任何锚走到。「绿」只度量「与被测实现一致」，不度量「与规格一致」。

## 处置

1. 锚值从规格推导并附源码佐证（符号语义引 detector/perp_funding.go 同源注释），不从旧实现输出反推。
2. fixture 复刻真实消费形态：新增 `seedFundingPosition`（`:perp/:spot` 腿、PERP_FUNDING 类型），禁再借 `:mt5` 腿 fixture 测 funding。
3. 先红后绿：新锚在旧码上必须红（A1-A4/A7 实证 5 红），证明锚确实捕获缺陷而非平移现状；fail-closed 语义不变的场景（A5/A6）明示为回归锚，不虚报红。
4. 变异对抗证灵敏度：翻转符号一行 → A1/A2 红 → 还原 → 绿（证锚对缺陷方向敏感，非恒真断言）。

## 证据

- check: 新 A1-A7 锚跑旧码 `go test -run TestRecalcNetFunding_A ./internal/autotrade/`；再跑新码；再做符号变异复跑
- result: 旧码 exit=1（A1 want 6 got −6 / A3 want 8 got 0 / A4 want ok=true got false / A7 got ok=true val=−6）；新码 exit=0 全绿；变异后 exit=1（A1/A2 红）还原后 exit=0
- ref: D-312、commit 602a7fc7、提示词 docs/D-312设计与施工提示词-recalcNetFunding逐腿口径-2026-09-17-Devin.md @03568a15 §1「测试反讽」
- check: 2026-09-23 单日三案「规格文档自身例值与规格公式口径矛盾」——实现忠实规格仍错且先红后绿全绿：D-404（S1a「|Δt| 最近行」取开仓后 14s 行 vs §2 数值验证用开仓前行 7.73，capture=−0.47 生产提前收割）；D-412 B1（§3 恒等式 accrued−fee+conv 减号 vs §4 红锚 est_fee=−6 负值记法，5−(−6)+0.5=+11.5≠−0.5）；D-425 R1（「保证金 10%」实现按 1x 名义读=8% 价格带 vs pin 的 10x=1% 带，差 10×）
- result: 三案均在 R0/R1 评审以「例值用规格公式在同一真实输入上重算」抓获（INJ 11−(−64.88)=75.9≈显示 76 算式闭合；5−6+0.5=−0.5 才是红锚本意；0.8×0.10=8% vs 0.8×0.10/10=0.8%）
- ref: D-404（c28f82fa/复审 882e2841）、D-412（评审增量 9cd9aba9）、D-425（R1 返修单 d1148dac 口径终裁）

## 反证条件

- check: 检查是否存在「锚值由规格独立推导且 fixture 同构真实场景，但缺陷仍全绿存活」的反例
- falsifies_when: 某符号/口径级缺陷在满足上述四条纪律的测试集下依然全绿通过（说明纪律不充分）

## 适用边界

- 适用于数值口径/符号/单位类缺陷与带形态敏感 fixture 的函数级测试；不适用于纯展示文案类改动。
- 「先红后绿」对 fail-closed 回归锚（守卫语义不变路径）不可达红，应明示标注而非伪造红轮。
- 变异对抗抽样一行即可证灵敏度，不要求穷举变异算子。
