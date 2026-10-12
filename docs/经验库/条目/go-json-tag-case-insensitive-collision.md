---
schema_version: 2
id: go-json-tag-case-insensitive-collision
kind: pitfall
risk: high
status: verified
keywords: [Go, encoding/json, struct tag, 大小写不敏感, 字段碰撞, ParseFloat, drop field N, parsing "USDT", binance, ORDER_TRADE_UPDATE, executionReport, 丢帧]
scope: [internal/collect/exchange, binance, venue 帧解码, Go encoding/json]
summary: Go encoding/json 结构体 tag 匹配大小写不敏感——帧内同现仅大小写不同的键（n/N、i/I、x/X、z/Z、ap/AP）时后写覆盖先写，佣金金额被币种 "USDT" 覆盖→ParseFloat 崩→fail-closed 整帧丢→OrderEventCache 恒空；修=map[string]json.RawMessage 精确键提取（map 键大小写敏感），键名即语义与官方 schema 逐键对齐。
first_seen: 2026-09-30
last_verified: 2026-09-30
skills: [KB维护]
evidence_refs:
  - type: commit
    ref: 1f4e162b
  - type: doc
    ref: internal/collect/exchange/binance_user_ws_events.go:74
  - type: doc
    ref: docs/施工-D-584-binance订单事件nN大小写碰撞丢帧修复-施工提示词-2026-09-30-Devin.md@b51a8c61
---

## 事实/症状

Binance 用户流每笔订单必打 `[binance-ws] drop field N: strconv.ParseFloat: parsing "USDT": invalid syntax`，`ORDER_TRADE_UPDATE` 帧到达 sink 且落审计表，但 `OrderEventCache.PeekOrder` 全 miss——事件驱动查单链形同虚设（REST 兜底掩盖）、FeeUSD 事件源断供。同族隐性雷：`i`/`I`（订单号/ExecutionId）、`x`/`X`（执行类型/状态）、spot `z`/`Z`（累计量/累计报价额）、`ap`/`AP`（均价/激活价）——部分靠帧内键序侥幸正确（隐性正确非设计正确），键序随事件类型漂移即翻车（测试实证：键序翻转后状态被读成 `x`=TRADE）。

## 根因或策略解释

Go `encoding/json` 结构体字段匹配**大小写不敏感**：tag `json:"n"` 同时命中帧内 `"n"`（佣金金额）与 `"N"`（佣金币种），同帧多键命中时**后出现的键覆盖先者**——生产帧 `N` 在 `n` 后 → 字段终值=`"USDT"`。struct tag 无法表达「精确大小写匹配」，凡同帧存在仅大小写不同的键的 schema 均同雷（交易所订单/账户事件键对密集：s/S、c/C、l/L、n/N、i/I、x/X、z/Z）。

## 处置

订单体解码从「struct tag 绑定」改为 `map[string]json.RawMessage` **显式键提取**（map 键大小写敏感，`m["n"]`/`m["N"]` 物理分离）：包内 helper `jstr`/`jnum`（缺失键返空值，保持 D-508 fail-closed 丢帧语义与 drop 日志——日志键名同步改 wire 键名）；消费键与官方 schema 逐键对齐（i=订单号 c=客户端单号 X=状态 z=累计量 ap=均价 L=末价 n=金额；N=币种不消费不解析）；键序无关整类歼灭，不接受「只修 N」局部补丁。

## 证据

- check: bntesttrade -live 两轮真开平（TRXUSDT 16 张）+ 新测 TestOrderUpdateNvsN/TestOrderUpdateFieldOrder/TestSpotExecutionReportNvsN 先红后绿
- result: 修前 PeekOrder 全 miss（drop field N）；修后命中且 FeeUSD=0.00267272、键序翻转变体同值；exchange 整包 -race 带 DSN 全绿
- ref: commit 1f4e162b（D-584）；键形官方 binance-spot-api-docs 仓（spot i=Order Id/I=Execution Id 并存）+ go-binance futures 真实帧夹具（i=order id 小写、AP=激活价并存）双源钉死

## 反证条件

- check: 若 Go encoding/json 未来改为大小写敏感匹配，或某 venue 帧保证不出现仅大小写不同的键对
- falsifies_when: struct tag 解码在 n/N 双键帧下长期取值正确且键序无关

## 适用边界

适用一切「同帧存在仅大小写不同键」的 venue 帧结构体解码（各所订单/账户事件高发）；`ACCOUNT_UPDATE` 的 `a.B`/`a.P` 元素键实测全小写（a/wb/cw/bc、s/pa/ep/cr/up/mt/iw/ps/ma/bep）无碰撞故不在此列。教训泛化：**字段名即语义的 wire 协议禁依赖大小写不敏感匹配**——同族坑=binance-ws-stream-name-case-sensitive（流名大小写敏感反向案例：规范化毁流名）。
