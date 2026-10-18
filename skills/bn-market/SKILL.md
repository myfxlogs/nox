---
name: bn-market
description: binance spot 行情域端点能力账（D-608）——公开面零凭据即达
---
# bn-market
## 证据
- `docs/能力账/bn-api-probe-2026-10-18-o0-b095803.pb`（api 域行情段——USED-WEIGHT 均值 271 实测）
- 清单=`docs/能力账/binance_endpoints.gen.txt`
## 实测 OK 采样
- `GET /api/v1/ping`（w=46）`GET /api/v1/time`（w=46 族）`GET /api/v3/ticker/24hr`/`bookTicker`/`price`（w≈126-134）`depth`/`aggTrades`/`trades`/`historicalTrades`（w≈41-191）全活
- **权重值真值**：ticker/24hr 单符号 w≈126（USED-WEIGHT 实测——官方表记 40 为未含 tickers 变体；**规划按实测值×1.5 裕量**）
## 坑注记
- **D-004 实时性**：行情数据道正道=qbus WS 流（binance:ws-* 键）——REST 本表端点仅备用面非生产径。
- **WS 流族裁断**（10-18 实证）：`!markPrice@arr`/`!ticker@arr` 聚合流 ack 收讫零帧=venue 断供形态（ack≠存活——存活判据=帧戳前移）；`!bookTicker` 活。markPrice 家族 venue 断供期内 REST `premiumIndex` 兜底轨在供。
