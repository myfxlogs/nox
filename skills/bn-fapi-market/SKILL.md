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
