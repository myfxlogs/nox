---
schema_version: 2
id: grpc-server-stream-reconnect-on-break
kind: pitfall
risk: high
status: verified
keywords: [grpc, server-streaming, OnQuote, stream reconnect, 流断, GOAWAY, mt5, mtapi, 报价停摆, 现价失真]
scope: [internal/adapter, mt5, mt4, mtapi 网关, 任何 gRPC server-streaming 消费方]
summary: gRPC server-streaming 流（OnQuote/OnOrderProfit 等）Recv 出错后只 log+return 不重连，网关 GOAWAY/EOF 后流永久断开，下游报价/事件停摆，持仓现价与实时浮盈失真（隐蔽故障——一元 RPC 采集路径不受影响故 venue 看似活着）
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-101
  - type: commit
    ref: 4304388
  - type: doc
    ref: internal/adapter/mt5.go:250-268
  - type: doc
    ref: docs/OnQuote流重连方案-2026-08-25.md
  - type: server_fact
    source: rpc
    query: ListBrokerPositions refresh=true #264
    observed_at: 2026-08-25T01:58:00Z
    response_hash: ""
---

## 事实/症状

- #264 两条腿 `currentPrice=None, fresh=None`，浮盈用 venue 透传陈旧 `Order.Profit`（非实时重算）。
- qbus.Latest：AUDJPY 报价存在但 timestamp 停在 2026-08-24 16:52 UTC，超 `freshTTL=120s`。
- ListLatestQuotes：全部 286 条 mt5 报价同停（CEX 报价正常）。
- 日志铁证：16:52:07 UTC mtapi 网关 GOAWAY 切断 5 venue OnQuote + Margin 流；Margin 流（D-099 已有重连）22:18 再断有重连日志，OnQuote 流 16:52 后零重连日志。
- swap 采集走一元 RPC（SymbolParams/AccountSummary）不受影响 → 「venue 看似活着但报价停了」隐蔽故障。

## 根因或策略解释

`mt5.go` OnQuote `Subscribe` 内 `stream.Recv()` 出错后只 `log+return`，goroutine 退出、ch close，无重连机制。gRPC server-streaming 流在网关 GOAWAY/EOF/连接重置时必然断开，消费方必须自建重连（外层 for + 内层 Recv 循环 + 断流退避重开），否则一次断流=永久停摆。对比：同项目 margin 流（D-099 `mt_margin_source.go`）已有 consumeStream 重连模式，OnQuote 流漏建。

## 处置

复用 D-099 consumeStream 模式：`Subscribe` 抽 `runQuoteStream`（外层 for：每轮 resubscribeAll + OnQuote 开流 + consumeQuoteFrames）+ `consumeQuoteFrames`（内层 Recv 循环，err 时 IsSessionLost→reconnectSession + 退避 + return true 触发外层重开）。订阅集镜像（subMu 保护，重连后 resubscribeAll 恢复网关侧订阅——订阅集是重连充要条件，无它则重连后空订阅）。复用 S1 `IsSessionLost`/`reconnectSession` + `marginReconnectBackoff`（1s/5s/30s，同因同退避不新增 var）。MT4 同构改造（含 reconnectSession，proto 字段差异 User int32 vs uint64）。4 单测先红后绿覆盖：ReconnectAfterRecvErr / ResubscribeSurvivesReconnect / CtxCancelClosesCh / SessionLostTriggersReconnect。

## 证据

- check: `go test ./internal/adapter/ -race -run TestQuoteStream -count=1`
- result: ok arb/internal/adapter 1.045s（4 用例 + race 全绿）
- check: RPC ListBrokerPositions refresh=true #264（部署后 2026-08-25T01:58Z）
- result: 两腿 currentPrice=113.9355/113.936 fresh=true，pnl 实时重算（SELL -0.43 + BUY +0.419，swap 9.97）
- check: RPC ListLatestQuotes AUDJPY（部署后）
- result: 5 venue（277842155/52993526/REDACTED_MT_ACCT/39050818/1314615）报价 time=2026-08-25T01:57:45Z 全 fresh（<120s TTL）
- check: `docker logs arb-core 2>&1 | grep -ciE "onquote.*err|onquote.*reconnect"`
- result: 0（流稳定运行无断流无重连）
- ref: D-101 / commit 4304388 / docs/OnQuote流重连方案-2026-08-25.md

## 反证条件

- check: 部署后再次出现 mt5 报价全停（ListLatestQuotes mt5 venue time 超 freshTTL）且日志无 onquote 重连记录
- falsifies_when: 重连机制未生效（如 IsSessionLost 未覆盖实际 GOAWAY 错误码，或退避过长导致长时间无报价）

## 适用边界

- 适用于所有 gRPC server-streaming 消费方（OnQuote/OnOrderProfit/任何 Recv 循环）——流断必重连是通用不变量。
- 订阅集镜像仅适用于「网关侧需显式 Subscribe」的流（OnQuote）；无状态流（如纯推送）无需。
- `IsSessionLost` 覆盖范围见 S1（UNAVAILABLE/GOAWAY 等）；未覆盖的错误码走纯退避重开（不重建 session）。
- **未覆盖变体（9-22 实证）**：流无 Recv error、只吐 `{}` 心跳零真实帧=终端侧推送死，重连机制够不着——见 `mtapi-onquote-zeroframe-terminal-dead`。
- 本条目=项目内闭环（D-101 部署验证完成）；可泛化模式「server-streaming 流断必重连 + 订阅集镜像」已具备晋升 ~/kb 条件（待收工判断）。
