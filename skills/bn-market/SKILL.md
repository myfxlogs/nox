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
- **能力探测频控注记**（注记#74②）：probe 批跑须预留生产权重余量——预检 ping=存活验证≠频限合规（418 封禁窗内 ping 仍 200 实证）；批跑限速窗（pace ≥500ms）+错峰生产高负载窗+418 即停 abort 禁续扫——烧穿共享出口 IP 权重=REST 兜底全灭形态（10-19 实证 37 次/4min）。
- **端点级记实（o0 残段补跑批 b135758/b135803）**：13 残端点全采=5 ok/5×400 缺参族/3×404——400 缺参族=裸跑无参数占位预期败（时间窗/symbol 定向类端点必带参）；权重闸接线坑=「闸件并入未接线首跑实证截批 used=1248>cap」详见 `docs/经验库/条目/bnprobe-weightcap-merged-unwired.md`。
