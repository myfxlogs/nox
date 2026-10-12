---
schema_version: 2
id: gate-ws-private-channel-payload-shapes
kind: pitfall
risk: high
status: verified
keywords: [gate, gateio, websocket, futures.balances, futures.positions, futures.orders, futures.usertrades, subscribe, payload, need 1 user id, need market param, 私有频道]
scope: [internal/collect/exchange, gate futures WS, 私有频道订阅, D-450]
summary: Gate futures 私有 WS 订阅 payload 形制按频道分派——balances 单参 [uid]，positions/orders/usertrades 双参 [uid,"!all"]；照搬文档统一形制会 8 发全拒
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [事实核验]
evidence_refs:
  - type: server_fact
    source: gate futures WS wss://fx-ws.gateio.ws/v4/ws/usdt
    query: 8 发全表实测（4 频道 × 2 形制）
    observed_at: 2026-09-24T00:00:00Z
  - type: doc
    ref: internal/collect/exchange/gate_private_ws.go subscribeFrame
---

## 事实/症状

Gate futures 私有 WS（`/v4/ws/usdt`）订阅四个私有频道时，`payload` 形制**不是**统一的：

- `futures.balances` → `payload: ["<uid>"]`（单参）；给 `["<uid>","!all"]` 报 **"need 1 user id"**。
- `futures.positions` / `futures.orders` / `futures.usertrades` → `payload: ["<uid>","!all"]`（双参）；给 `["<uid>"]` 单参报 **"need market param"**。

8 发全表实测（4 频道 × 2 形制）实证，错误消息直接指明缺/多哪个参。

## 根因或策略解释

Gate 官方文档示例给的是 `payload=[uid,"!all"]`，但未逐频道标注差异——balances 频道不接受市场过滤器参数（账户级，无 market 维度），其余三频道强制要 market 参数（`!all`=全部合约通配）。按文档统一双参会令 balances 订阅被拒，订阅 ACK 为错误响应而非静默。

## 处置

`subscribeFrame` 按频道分派 payload：`channel=="futures.balances"` → `[uid]`，其余 → `[uid,"!all"]`（见 `internal/collect/exchange/gate_private_ws.go`）。测试 `TestGateSubscribeFrame` 表驱动断言四频道两形制。

## 证据

- check: 生产实证矩阵——经 cex-proxy `/v4/ws` 对 gate 真 key 逐频道逐形制发 8 帧
- result: balances+[uid,"!all"]→"need 1 user id"；positions/orders/usertrades+[uid]→"need market param"；正确形制全部订阅成功
- ref: D-450 返修（@916bfa2 指令）；seat/devin-d450

## 反证条件

- check: gate 文档改版或错误消息变化后重发矩阵
- falsifies_when: balances 接受 `!all` 或其余频道接受单参（即形制收敛统一）

## 适用边界

仅 Gate futures USDT 私有 WS（`/v4/ws/usdt`）。spot/其他 settle 区（BTC）未实测；OKX/binance 私有流形制各自独立（binance 走 listenKey 无订阅帧）。签名串 `channel=…&event=subscribe&time=…` 不含 payload，形制差异不影响签名。
