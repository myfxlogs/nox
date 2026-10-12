---
schema_version: 2
id: binance-ws-stream-name-case-sensitive
kind: pitfall
risk: high
status: verified
keywords: [binance, websocket, fstream, bookTicker, markPrice, 流名大小写, ToLower, ack成功零帧, 静默断流]
scope: [internal/collect/exchange, binance, cex-proxy, /public/ws, /market/ws]
evidence:
  source: cex
  query: wscat 探针订阅 btcusdt@bookTicker vs btcusdt@bookticker、@markPrice vs @markprice
  observed_at: 2026-09-27T04:00:00Z
  server_revision: "D-517 端点迁移后拓扑（/public /market /private 三路）"
  response_hash: ""
---

## 事实/症状

Binance USDⓈ-M 期货 WS 迁移后端点（`/public/ws`、`/market/ws`）上流名**大小写敏感**：订阅 `btcusdt@bookticker`/`btcusdt@markprice`（全小写）返回 `{"result":null,"id":N}` ACK 成功，但**零数据帧、无 error、无断线**——与旧端点 `/ws` 退役后的静默断流形态逐字一致。`@bookTicker`/`@markPrice` 驼峰原名正常推帧。

## 根因或策略解释

D-517 引入 `kind` 参数构造流名时写成 `strings.ToLower(sym+"usdt")+"@"+strings.ToLower(kind)`——符号小写正确，但 kind 被连带压小写。订阅循环因此静默杀死三条连接（public/market/spot 全灭），现象=连接活着、无日志、无 facts 落库。

## 处置

流后缀 kind 保字面量不规范化：`strings.ToLower(sym+"usdt")+"@"+kind`。教训泛化：**venue 协议标识符（流名/频道名/事件名）一律字面常量，禁过 ToLower/ToUpper 规范化管线**——规范化只作用于己方符号位。

## 证据

- check: 生产代理实探四连——`@bookTicker`=415 帧/3s、`@bookticker`=ack 零帧、`@markPrice`=6 帧/3s、`@markprice`=ack 零帧
- result: 修复后 `binance_ws_markprice` facts 342 行/20s 新鲜、binance ticks 落库
- ref: commit 35f36c20（D-517 部署回归修复）、merge 206bdeb0

## 反证条件

- check: 若 Binance 改为大小写不敏感路由，全小写订阅也应出帧
- falsifies_when: 探针对 `@bookticker` 收到数据帧

## 适用边界

适用于 Binance USDⓈ-M WS 新端点拓扑（2026-04 迁移后）；spot ws 与旧 `/ws` 行为未逐字复核。同坑模式可外推一切 venue 订阅名拼接处（okx channel 名、gate channel 名同理禁规范化）。
