---
schema_version: 2
id: stream-field-one-clock-law
kind: invariant
risk: high
status: observed
keywords: [流式, 两钟拼接, 一帧一钟, MT5, OnOrderProfit, 现价, 盈亏, ant, 原子发布, 客户端跳=面板跳, D-571]
scope: [显示层事件化, 采集面设计, MT5/CEX 持仓展示]
summary: 业主法条（2026-09-30）：「MT5 客户端在跳=面板必须跳；不管数据库里有没有，数据流展示出来」——流内字段禁止第二源拼接（两钟拼接禁令）。同一条显示行的字段若来自两条更新路径（venue 帧 vs 报价总线拼接）必然漂移分家（实证：同帧现价与盈亏隐含价差 2.4$）。参照系=ant backend fetchAndPublish 每帧原子发布（AccountSummary+帧内全仓+单一 CapturedAt 一帧一钟，REST 兜底 CurrentPrice=Zero 诚实标记）。
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [市场研究]
evidence_refs:
  - type: decision
    ref: D-571
  - type: doc
    ref: internal/dashboard/service_broker.go:273
  - type: doc
    ref: internal/dashboard/broker_position_monitor.go:205
  - type: doc
    ref: internal/adapter/mt_quote_stream.go:112
---

## 法条

展示行字段必须**一帧一钟**：同帧到达的字段同刻渲染；帧内没有的字段走诚实空态（「—」/留空），**禁止**从另一条通道（报价总线/DB 快照/定时器）拼接补齐——两条路径两个延迟，拼出来的行自相矛盾（业主实测 ticket 2555274799：现价 4144.78 vs 盈亏隐含 4147.2 vs 实时 4147.9）。

## 三病灶形态（本项目实证）

1. **适配层丢字段**：mtapi Order 帧无 PriceCurrent（全字段实证），现价只能来自报价流——但适配层连帧内可带的价上下文都未映射（VenueOrder 无价字段）；
2. **展示层拼接**：`service_broker.go:273` 现价=(Bid+Ask)/2 另取报价源 + TTL 闸；`broker_position_monitor.go:205` onQuote 脏化→flush 重取 LatestLeg——盈亏走 venue 帧、现价走第二钟；
3. **拼接匹配断点**：`mt_quote_stream.go:112` publishSymbol 变体组非 primary 降格原生键发布（XAUUSDm）vs 持仓 pb.Canonical（XAUUSD）→ onQuote 永不匹配 → 现价永久冻结而盈亏照跳。

## 参照实现（ant，业主指定）

`myfxlogs/ant backend/internal/mdgateway/adapter/mt5/profit.go fetchAndPublish`：每条 OnOrderProfit 帧 → **一次原子发布**（AccountSummary 权益+帧内全仓 Positions+单一 CapturedAt 时间戳打包一个消息）；无帧 REST 兜底时 `CurrentPrice=Zero`+`positionsAuth` 标记——一帧一钟+诚实空态双保险。D-571 设计吸收了 ant R1-R5 但漏了原子发布这条，包A 实现还引入两钟拼接=违此法条。

## 修法方向

①帧内有价→同帧直出（适配层映射）；②帧内无价→同次发布内取报价流并**钉帧时戳**（一帧一钟），拼接降缺席兜底；③匹配键序专项测试（变体/降格/mini 后缀场景 onQuote 必须仍命中）。

## 验收锚

行内字段同帧一致性（现价与盈亏隐含价差 ≤ 报价点差）+盯流 60s 现价 tick 计数 ≥ 行情 tick 密度（客户端跳=面板跳的机械化表述）。
