---
schema_version: 2
id: mt5-symgroup-margin-fields-not-served
kind: server_fact
risk: medium
status: verified
keywords: [mt5, mtapi, 网关, SymGroup, InitMarginRate, MntnMarginRate, margin, 保证金, 杠杆, Leverage, 无限杠杆, SymbolParams, proto定义不等于实装]
scope: [adapter, mt5, fact, ops, probe]
summary: MT5 网关 SymGroup margin 字段 proto 有定义但实拍不返回；账户级杠杆=AccountSummary.Leverage（Exness 无限杠杆=2e9 哨兵），保证金率=1/Leverage
first_seen: 2026-08-24
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: https://git.mtapi.io/root/grpc-proto/-/blob/main/mt5/protos/mt5.proto（SymGroup InitMarginRate=23/MntnMarginRate=24 定义）
  - type: server_fact
    source: mt5grpc3.mtapi.io
    query: MT5/SymbolParams（EURUSDm）+ MT5/SymbolParamsMany（EURUSDm,XAUUSDm）+ MT5/AccountSummary（5 账号）
    observed_at: 2026-08-24
---

## 事实/症状

1. **proto 定义 ≠ 网关实装**：官方 proto 的 `SymGroup` 有 `InitMarginRate=23` / `MntnMarginRate=24`（repeated double），但直连网关 `SymbolParams` 单查与 `SymbolParamsMany` 批量返回的 SymbolGroup **均不含任何 margin 字段**（仅有 MinLots/MaxLots/LotsStep/GroupName/TradeMode/TradeType/FillPolicy/Expiration/OrderFlags/PriceTimeout/RequoteTimeout/MinVolume/MaxVolume/VolumeStep/SwapType/SwapLong 等）。只看 proto 设计「字段在手边」= 采集零产出（S6 原设计返修实证）。
2. **账户级杠杆可得**：`MT5/AccountSummary` 的 `Leverage`（float64）实拍全账号有值——277842155=2e9（**Exness 无限杠杆哨兵值，非异常**）、REDACTED_MT_ACCT=500、52993526=1000、1314615=100、39050818=100。
3. **无限杠杆语义**：Leverage=2e9 且 Margin≈0 = 零保证金账号（事实），init_margin_rate 应发 0 值（对照 SwapModeNone 先例：事实必须可配对，禁跳过）。

## 根因或策略解释

- mtapi 网关对 SymGroup 只实现了部分字段透传，margin 率类字段（repeated double 23/24）与 InitialMargin/MaintenanceMargin（21/22）均未实现——服务器侧值存在与否不可从 proto 推断。
- 替代路径：账户级 `1/Leverage`（Leverage 在 AccountSummary 可得）——账户级近似，符号级费率（黄金 5% vs 外汇 1%）网关不支持。

## 处置

- 设计符号级 margin/杠杆采集前：**先直连网关实拍 SymbolParams 响应**（流程见 [[mtapi-gateway-query-procedure]]），确认字段实装再定数据源。
- 账户级保证金率：`AccountSummary.Leverage` → `1/Leverage`；`Leverage<=0` 防 Inf 守卫；`Leverage>=1e6` 判无限杠杆哨兵发 0 值。
- 慢 venue（PXBT 类）AccountSummary 也可能 >3s 超时 → 失败只打日志下轮重试，勿进 errs 误伤 swapResult。

## 证据

- check: 2026-08-24 直连网关三连（SymbolParams EURUSDm 无 margin 字段 / SymbolParamsMany 同 / AccountSummary 5 账号 Leverage 全有值）+ 官方 proto 对拍（字段定义存在）
- result: S6 返修部署验证——init_margin_rate 入库 4/5 venue 值对照实拍杠杆全对（1314615=0.01/277842155=0/39050818=0.01/REDACTED_MT_ACCT=0.002），代码路径正确性实证
- ref: 会话 2026-08-24（S6 返修）；STATE 下一步①观察 52993526

## 反证条件

- check: 网关升级实装 SymGroup margin 字段后本条目过时
- falsifies_when: SymbolParams 响应出现非空 InitMarginRate → 符号级采集可行，条目更新为「网关已支持」
