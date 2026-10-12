---
schema_version: 2
id: ws-zombie-conn-pong-alive-data-dead
kind: pitfall
risk: high
status: verified
keywords: [websocket, 僵尸连接, pong续期, readIdle遮蔽, 静默断流, 零日志, wsrunner, okx, mihomo, TUN重启, 数据帧看门狗]
scope: [cex行情WS, okx_ws, gate_ws, binance_market_ws, wsrunner, gorilla/websocket, 代理链路切换]
summary: WS 连接控制面（ping/pong）存活但数据面断线时，pong 续期会遮蔽 readIdle 判活窗，连接僵尸化且零日志——需独立数据帧看门狗兜底。
first_seen: 2026-09-27
last_verified: 2026-09-27
skills: [KB维护, 事实核验]
evidence_refs:
  - type: commit
    ref: 0cfaa922
  - type: doc
    ref: internal/collect/exchange/ws_keepalive.go
  - type: doc
    ref: internal/collect/exchange/wsrunner.go
  - type: server_fact
    source: postgres
    query: SELECT src, max(ts) FROM facts WHERE src='okx_ws_v5' GROUP BY src
    observed_at: 2026-09-27T08:57:00Z
---

## 事实/症状

2026-09-27 mihomo 重启切流后，arb-core 的 `okx_ws_v5` fact 断流 18min 且**零日志、零 panic、零 reconnect 记录**。同容器的 gate-ws/binance-mkt-ws 正常打 `reconnect after error` 日志并恢复；`okx-ws` 连一条 dial/reconnect 日志都没有。`docker restart` 后 2s 复供。

## 根因或策略解释

`wsKeepalive` 的续期逻辑是「pong 到达即重置 60s 读限」（`SetPongHandler → SetReadDeadline`）。这区分不了两种状态：

- **控制面活 + 数据面活**（正常）
- **控制面活 + 数据面死**（僵尸：pong 正常续期但订阅帧不再来）

第二类场景下读限永远不触发——gorilla 内部消化 pong 帧并续期，应用层 `ReadMessage` 被 pong「遮蔽」，读循环永远等不到超时。上游侧常见成因：链路切换（TUN 重启/代理切换）时 TCP 层保活但 WS 订阅态丢失、或上游静默丢订阅。

关键盲区：**read deadline 只在 `ReadMessage` 阻塞时生效**；而 pong 续期让它永不超时。零日志因 `wsrunner` 只在 dial/重连事件打日志——僵尸连接不产生任何事件。

## 处置

`wsrunner` 增加**数据帧独立看门狗**（`WSSpec.DataIdle`，默认 `quoteWSDataIdle=10min`）：

- 读循环每帧更新 `lastDataAt`（atomic）
- ping goroutine 每 tick 检查：数据帧缺席 > DataIdle → `conn.Close()` → 读循环报错 → 走既有退避重连路径
- 默认 10min 须 `>>` ReadIdle 60s：安静行情窗（低流动符号 legitimately 无推送）由 pong 续期语义承接不误杀；10min 全静默对多符号聚合 tickers 流即真异常

配套：`okx_ws.handleMessage` 对 `event=error` 帧打日志（此前静默吞，订阅被拒不留痕）。

## 证据

- check: `docker logs arb-core | grep okx-ws`（容器存活期全程）+ facts 新鲜度查询
- result: 断流 18min 内 `okx-ws` 日志 0 条；restart 后 `okx=q:2s` 即时复供
- ref: commit 0cfaa922（seat/devin-wswatchdog）；TestWSRunner_DataIdleWatchdog 钉死语义

## 反证条件

- check: 构造 server 回 pong 但零数据帧，观察 upgrades 计数
- falsifies_when: DataIdle 窗内连接未重连（看门狗失效），或安静窗内正常连接被误杀（窗口过小）

## 适用边界

- 适用于所有走 `wsrunner` 骨架的公共行情 WS（okx/gate/binance 三所）
- 私有 WS（okx_private_ws 等）未走该骨架，其 keepalive 形态需各自确认
- 低频数据流（funding 事件按小时级）须靠 DataIdle 窗口 >> 事件间隔才不误杀——当前 10min 对 tickers 聚合安全，单符号稀疏流需个案评估
