---
schema_version: 2
id: cex-demo-funding-simulated
kind: pitfall
risk: medium
status: verified
keywords: [binance demo, demofapi, funding 仿真, mock trading, cex-proxy, 模拟盘]
scope: [binance, demo-fapi, testnet, carry 研究, 执行验证]
summary: binance demo/testnet funding 是仿真自产不镜像主网——demo 仓可验执行机制，不能验 carry 经济学
first_seen: 2026-09-17
last_verified: 2026-09-17
skills: [事实核验, 生产取证]
evidence_refs:
  - type: doc
    ref: tools/cex-proxy/main.go:53
  - type: server_fact
    source: cex
    query: GET /demofapi/fapi/v1/premiumIndex?symbol=<SYM> 经 cex-proxy
    observed_at: 2026-09-17T06:50:00Z
---

## 事实/症状

binance demo-fapi（Mock Trading）与 testnet.binancefuture.com 实测为同构环境（同 605 个 TRADING symbol、相同覆盖缺口）：PAXGUSDT/XAUUSDT 有，CLUSDT/XAUTUSDT 无。价格贴近主网（mark 差 ~0.02%），但 funding 不镜像：PAXG demo 0.01% vs 主网 0.005%、BTC demo 0.01% vs 主网 0.003%、ETH demo 0.0042% vs 主网 0.0018%——demo 撮合产生自己的 premium/funding 序列。

## 根因或策略解释

demo 环境的 order book 是仿真撮合，funding rate 由该仿真的 mark-index premium 驱动，与主网 funding regime 无关。价格因做市机器人跟踪主网而贴近，funding 则是「自己的天气」。

## 处置

- demo 仓用于验证执行机制（下单/成交/滑点/退出链路）——有效
- **不得**用 demo funding 实收推断真实 carry 收益——经济学验证走纸面账本 + 主网 `funding_history` 采集
- 接入路径：cex-proxy `/demofapi/fapi/v1/*` 路由已通（默认配置含）；exchange_configs 同 venue 双行会撞 isRealMap/executor 键——接入需先解决行结构（D-320 调研记录）

## 证据

- 2026-09-17 经 cex-proxy 实测 `/demofapi/fapi/v1/{ping,exchangeInfo,premiumIndex}` 全 200
- 逐 symbol funding 对比 demo vs 主网三对样本系统性背离
- gate testnet 同理待验证（pos 485 的 funding 实收是否仿真值——下次结算点对照主网同 symbol 判断）
