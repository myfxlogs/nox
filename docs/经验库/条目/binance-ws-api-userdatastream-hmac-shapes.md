---
schema_version: 2
id: binance-ws-api-userdatastream-hmac-shapes
kind: pitfall
risk: high
status: verified
keywords: [binance, ws-api, session.logon, userDataStream.start, userDataStream.ping, -2028, HMAC, Ed25519, listenKey, 410 Gone, userDataStream]
scope: [internal/collect/exchange, binance spot 用户数据流, ws-api 控制面, D-459]
summary: Binance spot ws-api 用户流 HMAC key 兼容路径=userDataStream.start/ping/stop 免签名 apiKey-only 三件套；session.logon 拒 HMAC（-2028 仅收 Ed25519），REST userDataStream 端点已 410 Gone
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [事实核验]
evidence_refs:
  - type: server_fact
    source: binance ws-api wss://ws-api.binance.com/ws-api/v3
    query: session.logon/userDataStream.start/ping/stop 真 key 逐形制实发（经外部代理机）
    observed_at: 2026-09-24T00:00:00Z
  - type: doc
    ref: internal/collect/exchange/binance_user_ws_spot.go
---

## 事实/症状

Binance spot 用户数据流 REST 端点 `POST /api/v3/userDataStream` 已下线（410 Gone，双 `/api` 路径同 410=端点死非路径错）。官方替代=ws-api（`wss://ws-api.binance.com/ws-api/v3`），但文档主推的 `session.logon`+`userDataStream.subscribe` 路径**只收 Ed25519 类型 API key**——HMAC key logon 被 `-2028 "HMAC-SHA-256 API key is not supported"` 拒绝，未认证连接 `userDataStream.subscribe` 报 `-1193`。

HMAC key 兼容路径实证（2026-09-24 外部代理机真 key 实发）：

- `userDataStream.start` params=`{apiKey}` **仅一参免签名** → 200 + 60 位 listenKey（加 timestamp/signature 报 -1101/-1104 参数数错）
- `userDataStream.ping` params=`{apiKey, listenKey}` 两参 → 200（加 timestamp 或 signature 均 -1104 未读参）
- `userDataStream.stop` 同 ping 形制 → 200
- listenKey 失效语义同 REST 时代（60min 不 ping 过期）；事件仍推 `stream.binance.com:9443/ws/<key>` 经典流端点，**不是** ws-api 连接本身

## 处置

`binance_user_ws_spot.go`：ws-api 控制连接（代理 `/wsapi` 前缀 strip → ws-api.binance.com）只做 start/ping 生命周期管理，事件走既有 `/spotws/ws/<key>` 流连接；续租失败/读断→重连重取 key。测试 `TestBinanceSpotWsapiE2E` 断言 start/ping 帧 params 精确键集。

## 证据

- check: 真 key 对 ws-api 逐形制实发 8 帧矩阵（logon/start×3 形制/ping×3 形制/stop）
- result: logon→-2028；start{apiKey}→200 listenKey；start+签名→-1101；ping{apiKey,listenKey}→200；ping+timestamp 或 +signature→-1104
- ref: D-459 S0 取证；seat/devin-d459

## 反证条件

- check: binance 文档改版或 HMAC key 类型升级后重发矩阵
- falsifies_when: session.logon 接受 HMAC key，或 start/ping 要求签名参数（届时形制收敛需重实证）

## 适用边界

仅 Binance **spot** ws-api 用户数据流管理面。perp 用户流走 fapi listenKey REST（仍活，不受影响）；ws-api 交易面（order.place 等）签名形制未实测属另一域；Ed25519 key 未持有未验证 logon 路径；margin 用户流 2026-02 起改 listenToken 机制与 spot 不同。
