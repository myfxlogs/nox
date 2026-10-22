---
name: bn-fapi-market
description: binance USDⓈ-M 合约行情域端点能力账（D-608）
---
# bn-fapi-market
## 证据
- `docs/能力账/bn-api-probe-2026-10-18-o0-b095803.pb`+`o150.pb`（fapi 行情段）
## 实测 OK 采样
- `GET /fapi/v1/ping`/`time`/`exchangeInfo`/`ticker/24hr`/`ticker/bookTicker`/`ticker/price`/`premiumIndex`（interestRate+nextFundingTime 字段齐=funding 面活）`openInterest`（symbol+openInterest+time）`klines`/`depth`/`rpiDepth` 全活
- `globalLongShortAccountRatio`/`topLongShortPositionRatio`/`takerlongshortRatio` 情绪面 ok（周期参数缺=400 如实缺）
## 坑注记
- **markPrice 流族断供**（10-18 在案）：`!markPrice@arr[@1s]`/`!ticker@arr` venue 停推=REST `premiumIndex` 兜底轨顶供 funding facts——解禁判据「帧戳前移」守。
- **/fapi vs /fapi/v1 路径**：v1 全谱实测；v2 账户/仓位签名面（在 bn-fapi-account）。
- **`premiumIndex` = funding/mark/lastPrice 三位**——funding 下个时点+interval 字段都在此端点，别拆多调用。
- **能力探测频控注记**（注记#74②）：probe 批跑须预留生产权重余量——预检 ping=存活验证≠频限合规（418 封禁窗内 ping 仍 200 实证）；批跑限速窗（pace ≥500ms）+错峰生产高负载窗+418 即停 abort 禁续扫——烧穿共享出口 IP 权重=REST 兜底全灭形态（10-19 实证 37 次/4min）。
- **bookTicker 每连接流数阈=1（10-21 四变体探针实锤；10-22 口径修正「N=1 硬阈」→「epoch 概率裁断」）**：fstream 连接挂载 >1 条 `<sym>@bookTicker` 流=venue ack 收讫后短窗强杀（close 1006，6s~50s/轮，零业务帧）——帧形态无关（4条/帧×分批、1条/帧×8 连发、官方合并流 URL `stream?streams=` 全灭）；**N=2 合并流也 6s 被杀=阈值恰为 1 非突发**。**唯一存活形=每连接单流**：`!bookTicker` 聚合流（产线钦定形，~5s 批量量子桶——40s 窗 BTCUSDT 仅 8 帧实证）或单符号 `<sym>@bookTicker`（10-22 实测：rawpath 27.1s/SUBSCRIBE-6 32.5s/1conn1sym 174.5s 概率裁断——**epoch 概率裁断 28-175s 非硬阈，重连环必备**；spot `stream.binance.com:9443/ws/<sym>@bookTicker` 270s 全窗零杀 45,473 帧≈168fps=文档正典形）。**推论**：watchlist/armed 腿亚秒个体流=一符号一连接+重连环（被杀=常态非异常），聚合流保留=全宇宙面+断窗托底（D-531「突发杀」应读作「每连接多流杀」——探针=`tools/bn-indiv-wsprobe` 在库）。产线逐符号增订件=`binance_market_ws_indiv.go`。
- **端点级记实（o0 残段补跑批 b135758/b135803）**：5×400 缺参族=周期/定向参数必带端点裸跑预期败；权重闸接线坑详 `docs/经验库/条目/bnprobe-weightcap-merged-unwired.md`。
