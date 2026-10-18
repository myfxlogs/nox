---
name: bn-sapi-wallet-earn
description: binance sapi 钱包+earn 域端点能力账（D-608）——最大端点面
---
# bn-sapi-wallet-earn
## 证据
- `docs/能力账/bn-api-probe-2026-10-18-o150.pb`（sapi n=114 ok=53）+`o300.pb`（sapi n=150 ok=55）+`o450.pb`（sapi n=118 ok=35）——三批累计 sapi 面 382 端点/143 ok
## 实测 OK 采样（分域）
- **账户状态**：`/sapi/v1/account/apiRestrictions`/`apiTradingStatus`/`info`/`status` 全活
- **资产**：`asset/assetDetail`/`assetDividend`/`dribblet`/`tradeFee`/`wallet/balance`/`capital/config/getall`（充值网络面全活）
- **algo 族**：`algo/{spot,futures}/{openOrders,historicalOrders,subOrders}` 六件全活
- **bfusd**：`/sapi/v1/bfusd/account|quota|history/{rateHistory,redemptionHistory,rewardsHistory,subscriptionHistory}` 全活
- **loan vip**：`loan/vip/{loanable/data,ongoing/orders,repay/history,request/data}` 活
- **margin 读面**：`margin/{account,allAssets,allPairs,crossMarginCollateralRatio,crossMarginData,delist-schedule,isolated/*,priceIndex,risk-based-liquidation-ratio,tradeCoeff}` 活
- **onchain-yields**：`onchain-yields/account|locked/{list,position,history/*}` 活
- **pay/portfolio/mining/localentity**：pay/transactions·portfolio/{asset-index-price,collateralRate,delta-mode,interest-history,margin-asset-leverage}·mining/{hash-transfer/config/details/list,pub/*}·localentity/{country/list,questionnaire-requirements,vasp} 全活
## 坑注记
- **earn history 正确端点**=`/sapi/v1/simple-earn/flexible/history/redemptionRecord`（错前缀 `/sapi/v1/earn/...`=404 实证在案）。
- **asset/transfer type 枚举**不含 earn 对（17 枚举清单在册=划转盲区根因——earn 划转走 `simple-earn` 族别拿 transfer 凑）。
- **CL_USDTUSDT 双缀拼接雷**（私募坑——名字面拼接禁直接 `sym+USDT`，须交易所清单实读）。
- **mutating**：`capital/withdraw`/`asset/transfer`/`universal-transfer`/`simple-earn/{subscribe,redeem}`/`convert` 全族黑名单零跑——划转/申赎面能力账=未实测如实缺。
- **能力探测频控注记**（注记#74②）：probe 批跑须预留生产权重余量——预检 ping=存活验证≠频限合规（418 封禁窗内 ping 仍 200 实证）；批跑限速窗（pace ≥500ms）+错峰生产高负载窗+418 即停 abort 禁续扫——烧穿共享出口 IP 权重=REST 兜底全灭形态（10-19 实证 37 次/4min）。
