---
schema_version: 2
id: reconcile-tolerance-negative-amount
kind: pitfall
risk: medium
status: verified
keywords: [对拍, 容差, 负金额, 绝对值, swap, commission, 对账, quality升级]
scope: [cashflows 对拍, 一切金额容差比较, swapledger/exec]
summary: 对拍/对账容差公式百分比×amount 未取绝对值→负金额侧界为负→|diff|≤负数永假→匹配永不命中、quality 永升不了 actual；容差一律 max(固定值, k×|amount|)
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: [KB维护, 验证与交接]
evidence_refs:
  - type: doc
    ref: internal/store/pgstore/cashflows.go:113
  - type: commit
    ref: 017ba2b
  - type: doc
    ref: docs/D-193-A扩包-施工提示词-审计-2026-09-04-Claude.md
---

## 事实/症状

D-193-A 扩包提示词 v1.0（ZCode）决策 3 定的对拍容差为 `|diff|≤max(0.01, 0.5%×amount)`。cashflows.amount_usd 按现金流方向带符号（P1-14 硬约束：swap 按实收正=收入、commission 按实付负=支出）——短腿 swap 金额为负时，`0.5%×amount` 为负数，`|diff|≤负数` 恒假：对拍永不命中，quality 永升不了 actual，S6 对拍目标静默自败（无报错）。

## 根因或策略解释

百分比容差公式隐含假设 amount 为正。凡金额可正可负的域（现金流/盈亏/funding），`k×amount` 必须写 `k×|amount|`，否则负值侧的界反转。同族错误：与 0 比较、开方前负数、百分比回撤基期为负。

## 处置

v1.1（017ba2b）改为 `|diff|≤max(0.01, 0.005×|amount|)`。Claude 自审（D-207 轮）中对照 P1-14 符号约定证实负金额真实存在，修正非过度设计。

## 证据

- cashflows.go:113 注释：「amount_usd 正负号按实际现金流方向保存……本函数不做符号翻转」
- 审计发现与修法：docs/D-193-A扩包-施工提示词-审计-2026-09-04-Claude.md（R1d 顺带修）+ 修复说明映射表
- 修复 commit 017ba2b（v1.1 决策 3）

## 反证条件

若某对拍域已证明金额恒非负（如数量 qty、名义额 |notional|），则原公式成立，本条不适用。

## 适用边界

一切金额容差比较（对拍/对账/告警阈值）。写公式时先问：该域的金额是否带符号？带符号→绝对值。

## 晋升判断

可泛化（任何金额比较域）但为单行算术规则，价值密度低，暂不晋升 ~/kb（当场判定，收工不再重判）。
