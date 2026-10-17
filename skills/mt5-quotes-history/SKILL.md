---
name: mt5-quotes-history
description: mtapi MT5 行情/历史/会话面——Symbols/SymbolParams/GetQuote 族/PriceHistory 族/IsSession 族实测能力与符号方言坑
---

# mt5 行情历史面（实测 2026-10-17，证据=docs/能力账/mt5-rpc-probe-2026-10-17.json）

| RPC | 功能 | ICR | EXR | XMR | 关键坑 |
|---|---|---|---|---|---|
| Symbols/SymbolList | 全符号表（ContractSize/方言名源） | ok | ok | ok | Symbols 15032313 慢（4.8s）——批量径限频 |
| SymbolParams(Many) | 单符号规格（points/CS/trade_mode/swap） | err（XAUUSD not exist） | err（XAUUSDm 才有） | ok | **硬坑：符号方言**——每 broker 后缀不同（EXR=m 尾实拍），symbol 名一律 venue SymbolList 实读禁猜；生产径走 symMap 原生名 |
| GetQuote/GetQuoteMany | 点价 | err（XAUUSD not exist） | err（同） | ok | 订阅后才有活价（mt5 无 poll 报价语义——Subscribe+Events 正道） |
| PriceHistory(Many/Today/Ex/HighLow/Month) | K 线史 | ok | ok | ok | TimeFrame 枚举 16408=M1 等（mtapi 数值枚举非 MT5 原生 ENUM）；Ex 族用 NumBars 非 To |
| IsTradeSession/IsQuoteSession(Many) | 会话内外判定 | ok | err（XAUUSD key miss） | ok | Many 版字段=Symbols 复数；session 表落库换算见 mt_sessions.go（server→UTC） |
| SymbolSessionsEx(Many) | 符号级会话表（报价/交易时段细目） | ok | err（方言同） | err | 同符号方言坑——Many 版入参=symbol 字符串数组 |
| GetTickValueMany/TickValueWithSize | 点值 | ok | err（Symbol not found） | ok | tick value 计提公式输入（InPoints 语义） |
| TickHistoryRequest/OnTickHistory/TickHistoryStop | tick 级历史（请求→帧流→停） | ok | ok | ok | 请求后 OnTickHistory 流回帧（短窗零帧=窗内无 tick 非故障）；历史补齐面合法拉取 |

**正道锚**：行情判定走 Subscribe+Events 流（D-004）；PriceHistory 族=历史补齐/统计（事件锚定拉取合法）。
**复跑**：同 mt5-account。
