---
schema_version: 2
id: quote-currency-notional-mismatch
kind: pitfall
risk: high
status: verified
keywords: [币种换算, JPY 计价, 名义额, notional, USDJPY, 交叉盘, qty 定尺寸, 高估, fx 换算]
scope: [exec, strategy]
summary: 报价计价币≠USD（JPY 交叉盘）时 qty×CS×price 得计价币名义被误当美元——AUDJPY 高估 ~150 倍；定尺寸必须经计价币→USD 单点换算，fx 缺失 fail-closed
first_seen: 2026-08-21
last_verified: 2026-09-22
skills: []
evidence_refs:
  - type: decision
    ref: 修复④施工提示词
  - type: server_fact
    source: postgres
    query: 前端弹窗实测 balance $1655.61 < min notional $11,374,700
    observed_at: 2026-08-21
---

## 事实/症状

资金弹窗「balance $1655 < min notional $11,374,700」——AUDJPY 报价 113.5 是**日元计价**，`最小手(1) × CS(100,000) × 113.5 = 1135 万`被直接当美元名义。真实美元名义 = 1135 万 JPY ÷ USDJPY(≈150) ≈ $75K（与 #264 历史 1 手名义吻合）。后果：任何账户余额都"资金不足"→ 每日弹窗骚扰 + qty 定尺寸（notional/ask）同病（USD÷JPY 币种混算）。

## 根因或策略解释

公式假设「报价即 USD」只对 USD 计价对（XUSDT/XUSD）成立；JPY 结尾交叉盘价格是日元计价。CS×price 的量纲是计价币/手，换美元必须经计价币→USD 汇率。设计时未审量纲（Claude 设计错误，施工忠实复制放大）。

## 处置

单点 helper `usdPerLot(price, CS)`：USD 计价直接乘；JPY 计价 ÷USDJPY（qbus 实时价）；其他/未知计价币 → fail-closed 跳过 + 日志，禁止硬编码汇率或旧值。名义额/最小手预检/qty 定尺寸全部经它。**量纲审查**进设计自审清单：涉及价格×数量的公式，逐项问计价币是什么。

## 证据

- check: 弹窗数值 $11,374,700 ÷ USDJPY ≈ $75K（264 实际 1 手名义量级）
- result: 修复④ A 项（两公式改造 + helper 单点）
- ref: 修复④施工提示词

## 反证条件

- check: 后续新增计价币对是否全部经 usdPerLot
- falsifies_when: 全量名义额路径统一走 helper 后 → 条目降级为量纲自审清单项

## 适用边界

所有价格×数量的名义额/保证金/qty 计算。单一 USD 计价系统不适用（但扩币种时复发）。
